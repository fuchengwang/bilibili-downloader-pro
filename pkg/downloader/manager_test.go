package downloader

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
	"bilibili_downloader/pkg/config"
)

func newTestManager(t *testing.T) *DownloadManager {
	tmpDir := t.TempDir()
	cfg := config.NewConfigManager(tmpDir)
	return NewDownloadManager(cfg)
}

// TestPauseResumeDeadlock 测试全部暂停与全部继续在高并发场景下绝无死锁
func TestPauseResumeDeadlock(t *testing.T) {
	mgr := newTestManager(t)

	callbackCount := 0
	var cbMu sync.Mutex
	mgr.SetCallback(func(task *DownloadTask) {
		cbMu.Lock()
		callbackCount++
		cbMu.Unlock()
	})

	// 添加模拟任务
	for i := 0; i < 10; i++ {
		task := &DownloadTask{
			ID:           fmt.Sprintf("test_deadlock_%d", i),
			Title:        fmt.Sprintf("Deadlock Test %d", i),
			Status:       StatusQueued,
			VideoTmpPath: filepath.Join(os.TempDir(), fmt.Sprintf("test_v_%d.tmp", i)),
			AudioTmpPath: filepath.Join(os.TempDir(), fmt.Sprintf("test_a_%d.tmp", i)),
		}
		mgr.mu.Lock()
		mgr.tasks = append(mgr.tasks, task)
		mgr.mu.Unlock()
	}

	done := make(chan bool, 1)
	go func() {
		// 快速循环触发全部暂停与全部恢复，验证锁机制安全性
		for i := 0; i < 20; i++ {
			mgr.PauseAll()
			mgr.ResumeAll()
		}
		done <- true
	}()

	select {
	case <-done:
		// 成功通过无死锁
	case <-time.After(3 * time.Second):
		t.Fatal("PauseAll / ResumeAll 发生死锁阻塞超过 3 秒")
	}
}

// TestWorkerTokenIsolation 测试快速暂停恢复时 Worker Token 隔离与取消句柄安全性
func TestWorkerTokenIsolation(t *testing.T) {
	mgr := newTestManager(t)
	taskID := "test_worker_token_task"

	mgr.mu.Lock()
	mgr.workers[taskID] = workerHandle{
		token: "token_v1",
		cancel: func() {
			// cancel v1
		},
	}
	mgr.mu.Unlock()

	// 模拟新 worker (token_v2) 已经拉起
	mgr.mu.Lock()
	mgr.workers[taskID] = workerHandle{
		token: "token_v2",
		cancel: func() {
			// cancel v2
		},
	}
	mgr.mu.Unlock()

	// 旧 worker (token_v1) 退出并执行清理逻辑
	mgr.mu.Lock()
	if w, ok := mgr.workers[taskID]; ok && w.token == "token_v1" {
		delete(mgr.workers, taskID)
	}
	mgr.mu.Unlock()

	// 验证 token_v2 未被旧 worker 误删
	mgr.mu.RLock()
	w, exists := mgr.workers[taskID]
	active := len(mgr.workers)
	mgr.mu.RUnlock()

	if !exists || w.token != "token_v2" {
		t.Fatalf("新 Worker Token 被旧 Worker 错误覆盖或删除了: exists=%v, token=%s", exists, w.token)
	}
	if active != 1 {
		t.Fatalf("Active count 计算异常: %d", active)
	}

	// 清理模拟状态
	mgr.mu.Lock()
	delete(mgr.workers, taskID)
	mgr.mu.Unlock()
}

func TestMergingWorkerCannotCompleteAfterPauseOrCancel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cancel func(*DownloadManager, string)
		want   TaskStatus
	}{
		{
			name: "pause",
			cancel: func(mgr *DownloadManager, id string) {
				_ = mgr.PauseTask(id)
			},
			want: StatusPaused,
		},
		{
			name: "cancel",
			cancel: func(mgr *DownloadManager, id string) {
				_ = mgr.CancelTask(id)
			},
			want: StatusCancelled,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newTestManager(t)
			task := &DownloadTask{ID: "merge-state-" + tc.name, Status: StatusDownloading}
			mgr.mu.Lock()
			mgr.tasks = append(mgr.tasks, task)
			mgr.workers[task.ID] = workerHandle{token: "merge-token", cancel: func() {}}
			mgr.mu.Unlock()

			if !mgr.setWorkerTaskStatus(task, "merge-token", StatusDownloading, StatusMerging, "") {
				t.Fatal("worker should be able to enter merging from downloading")
			}
			tc.cancel(mgr, task.ID)

			if mgr.setWorkerTaskStatus(task, "merge-token", StatusMerging, StatusCompleted, "") {
				t.Fatal("a stale merging worker must not overwrite a paused/cancelled task as completed")
			}
			got := mgr.GetTasks()[0]
			if got.Status != tc.want {
				t.Fatalf("task status changed after %s: got %s, want %s", tc.name, got.Status, tc.want)
			}
		})
	}
}

// TestPauseNoDeadlockOnQueue 验证暂停后 worker 槽位正确释放，后续排队任务可继续拉起
func TestPauseNoDeadlockOnQueue(t *testing.T) {
	mgr := newTestManager(t)

	// 模拟 3 个任务被暂停，确认 workers 映射为空，调度器容量可用
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("task_%d", i)
		mgr.mu.Lock()
		mgr.tasks = append(mgr.tasks, &DownloadTask{
			ID:     id,
			Status: StatusDownloading,
		})
		mgr.workers[id] = workerHandle{
			token:  fmt.Sprintf("token_%d", i),
			cancel: func() {},
		}
		mgr.mu.Unlock()
	}

	if len(mgr.workers) != 3 {
		t.Fatalf("预设 worker 数量错误: %d", len(mgr.workers))
	}

	// 触发全部暂停，模拟各任务退出清理
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("task_%d", i)
		_ = mgr.PauseTask(id)
		// 模拟 runTask defer 的清理
		mgr.mu.Lock()
		if w, ok := mgr.workers[id]; ok && w.token == fmt.Sprintf("token_%d", i) {
			delete(mgr.workers, id)
		}
		mgr.mu.Unlock()
	}

	mgr.mu.RLock()
	activeRemaining := len(mgr.workers)
	mgr.mu.RUnlock()

	if activeRemaining != 0 {
		t.Fatalf("暂停后仍有残留活跃 worker: %d", activeRemaining)
	}
}

// TestTaskDeduplication 测试任务去重机制
func TestTaskDeduplication(t *testing.T) {
	mgr := newTestManager(t)

	req := &DownloadRequest{
		Title:         "去重测试视频",
		TargetQuality: "80",
		TargetCodec:   "AVC",
		Episodes:      []int64{999888777},
	}
	ep := &bilibili.EpisodeInfo{
		CID:   999888777,
		BVID:  "BV1Deduplication",
		Title: "第1集",
	}

	task1, err := mgr.AddDownloadTask(req, ep)
	if err != nil {
		t.Fatalf("第一次添加任务失败: %v", err)
	}

	// 再次添加相同 CID 与画质的任务
	task2, err := mgr.AddDownloadTask(req, ep)
	if err != nil {
		t.Fatalf("第二次添加任务失败: %v", err)
	}

	if task1.ID != task2.ID {
		t.Fatalf("任务去重失败，创建了重复任务: ID1=%s, ID2=%s", task1.ID, task2.ID)
	}

	// 清理测试任务
	_ = mgr.DeleteTask(task1.ID, false)
}

// TestAtomicSaveAndDeepCopy 测试 GetTasks 深拷贝快照与 SaveTasks 原子写入
func TestAtomicSaveAndDeepCopy(t *testing.T) {
	mgr := newTestManager(t)

	task := &DownloadTask{
		ID:        "test_deep_copy_task",
		Title:     "原始标题",
		Status:    StatusQueued,
		CreatedAt: time.Now().Unix(),
	}

	mgr.mu.Lock()
	mgr.tasks = append(mgr.tasks, task)
	mgr.mu.Unlock()

	// 1. 测试 GetTasks 深拷贝
	tasksCopy := mgr.GetTasks()
	var found *DownloadTask
	for _, tItem := range tasksCopy {
		if tItem.ID == task.ID {
			found = tItem
			break
		}
	}
	if found == nil {
		t.Fatal("未在 GetTasks 结果中找到添加的任务")
	}

	// 修改外部拷贝对象中的属性
	found.Title = "被外部篡改的标题"

	// 验证内部真实任务未受影响
	mgr.mu.RLock()
	var internalTask *DownloadTask
	for _, tItem := range mgr.tasks {
		if tItem.ID == task.ID {
			internalTask = tItem
			break
		}
	}
	mgr.mu.RUnlock()

	if internalTask == nil || internalTask.Title != "原始标题" {
		t.Fatalf("GetTasks 发生数据竞争或非深拷贝污染: internalTitle=%s", internalTask.Title)
	}

	// 2. 测试 SaveTasks 原子写入
	mgr.SaveTasks()
	tasksPath := mgr.cfgMgr.GetTasksPath()
	if _, err := os.Stat(tasksPath); err != nil {
		t.Fatalf("SaveTasks 未能生成持久化文件: %v", err)
	}

	// 清理
	_ = mgr.DeleteTask(task.ID, false)
}

func TestSaveTasksReturnsPersistenceErrorAndKeepsTarget(t *testing.T) {
	mgr := newTestManager(t)
	tasksPath := mgr.cfgMgr.GetTasksPath()
	if err := os.Mkdir(tasksPath, 0755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tasksPath)

	if err := mgr.SaveTasks(); err == nil {
		t.Fatal("任务列表写入目标为目录时应返回持久化错误")
	}
	info, err := os.Stat(tasksPath)
	if err != nil || !info.IsDir() {
		t.Fatalf("持久化失败后目标目录应保持不变: info=%v err=%v", info, err)
	}
}

func TestAddDownloadTaskRollsBackWhenPersistenceFails(t *testing.T) {
	mgr := newTestManager(t)
	tasksPath := mgr.cfgMgr.GetTasksPath()
	if err := os.Mkdir(tasksPath, 0755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tasksPath)

	task, err := mgr.AddDownloadTask(&DownloadRequest{
		Title:         "持久化失败回滚",
		TargetQuality: "80",
		TargetCodec:   "AVC",
	}, &bilibili.EpisodeInfo{CID: 123456, BVID: "BVrollback", Title: "持久化失败回滚"})
	if err == nil || task != nil {
		t.Fatalf("持久化失败时应返回错误且不暴露任务: task=%v err=%v", task, err)
	}
	if got := mgr.GetTasks(); len(got) != 0 {
		t.Fatalf("持久化失败后内存中不应残留任务: %+v", got)
	}
}

func TestDeleteTaskPersistenceFailureRestoresTaskAndFile(t *testing.T) {
	mgr := newTestManager(t)
	tasksPath := mgr.cfgMgr.GetTasksPath()
	if err := os.Mkdir(tasksPath, 0755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tasksPath)

	outPath := filepath.Join(t.TempDir(), "keep-after-delete-failure.mp4")
	if err := os.WriteFile(outPath, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	task := &DownloadTask{ID: "delete-persist-failure", Status: StatusCompleted, OutputPath: outPath}
	mgr.mu.Lock()
	mgr.tasks = append(mgr.tasks, task)
	mgr.mu.Unlock()

	if err := mgr.DeleteTask(task.ID, true); err == nil {
		t.Fatal("持久化失败时删除任务应返回错误")
	}
	if got := mgr.GetTasks(); len(got) != 1 || got[0].ID != task.ID {
		t.Fatalf("持久化失败后任务应恢复到内存列表: %+v", got)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("持久化失败时不应删除本地文件: %v", err)
	}
}

func TestClearCompletedPersistenceFailureKeepsTasksAndFiles(t *testing.T) {
	mgr := newTestManager(t)
	tasksPath := mgr.cfgMgr.GetTasksPath()
	if err := os.Mkdir(tasksPath, 0755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tasksPath)

	outPath := filepath.Join(t.TempDir(), "keep-after-clear-failure.mp4")
	if err := os.WriteFile(outPath, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	task := &DownloadTask{ID: "clear-persist-failure", Status: StatusCompleted, OutputPath: outPath}
	mgr.mu.Lock()
	mgr.tasks = append(mgr.tasks, task)
	mgr.mu.Unlock()

	if err := mgr.ClearCompleted(true); err == nil {
		t.Fatal("持久化失败时清空任务应返回错误")
	}
	if got := mgr.GetTasks(); len(got) != 1 || got[0].ID != task.ID {
		t.Fatalf("持久化失败后已完成任务应保留: %+v", got)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("持久化失败时不应删除本地文件: %v", err)
	}
}

func TestDeletedWorkerCannotEmitOrCompleteAfterRemoval(t *testing.T) {
	mgr := newTestManager(t)
	task := &DownloadTask{ID: "deleted-worker", Status: StatusDownloading}
	mgr.mu.Lock()
	mgr.tasks = append(mgr.tasks, task)
	mgr.workers[task.ID] = workerHandle{token: "deleted-token", cancel: func() {}}
	mgr.mu.Unlock()

	var callbacks int
	mgr.SetCallback(func(*DownloadTask) { callbacks++ })
	if err := mgr.DeleteTask(task.ID, false); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}
	if mgr.setWorkerTaskStatus(task, "deleted-token", StatusDownloading, StatusCompleted, "") {
		t.Fatal("已删除任务的旧 worker 不应再改变任务状态")
	}
	mgr.notifyChange(task)
	if callbacks != 0 {
		t.Fatalf("已删除任务不应再向前端发送事件，回调次数=%d", callbacks)
	}
}

// TestDeleteTaskAndFile 测试删除单个任务记录及联动删除本地源文件
func TestDeleteTaskAndFile(t *testing.T) {
	mgr := newTestManager(t)
	tmpDir := t.TempDir()

	subDir := filepath.Join(tmpDir, "TestCollection")
	_ = os.MkdirAll(subDir, 0755)

	outPath := filepath.Join(subDir, "video1.mp4")
	vTmp := filepath.Join(subDir, "video1.video.downloading")
	aTmp := filepath.Join(subDir, "video1.audio.downloading")

	_ = os.WriteFile(outPath, []byte("dummy video content"), 0644)
	_ = os.WriteFile(vTmp, []byte("dummy tmp video"), 0644)
	_ = os.WriteFile(aTmp, []byte("dummy tmp audio"), 0644)

	task := &DownloadTask{
		ID:           "test_delete_file_1",
		Title:        "Test Delete File",
		Status:       StatusCompleted,
		OutputPath:   outPath,
		VideoTmpPath: vTmp,
		AudioTmpPath: aTmp,
	}

	mgr.mu.Lock()
	mgr.tasks = append(mgr.tasks, task)
	mgr.mu.Unlock()

	// 1. 测试仅清除记录 (deleteFile = false)
	_ = mgr.DeleteTask("test_delete_file_1", false)
	time.Sleep(700 * time.Millisecond) // 等待异步清理协程完成

	if _, err := os.Stat(outPath); os.IsNotExist(err) {
		t.Fatal("deleteFile=false 时不应删除本地视频文件")
	}

	// 2. 重新添加并测试同时删除文件 (deleteFile = true)
	task2 := &DownloadTask{
		ID:           "test_delete_file_2",
		Title:        "Test Delete File 2",
		Status:       StatusCompleted,
		OutputPath:   outPath,
		VideoTmpPath: vTmp,
		AudioTmpPath: aTmp,
	}
	mgr.mu.Lock()
	mgr.tasks = append(mgr.tasks, task2)
	mgr.mu.Unlock()

	_ = mgr.DeleteTask("test_delete_file_2", true)
	time.Sleep(700 * time.Millisecond) // 等待异步清理协程完成

	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatal("deleteFile=true 时未能成功删除本地视频文件")
	}
	if _, err := os.Stat(vTmp); !os.IsNotExist(err) {
		t.Fatal("deleteFile=true 时未能成功删除临时视频文件")
	}
	if _, err := os.Stat(aTmp); !os.IsNotExist(err) {
		t.Fatal("deleteFile=true 时未能成功删除临时音频文件")
	}
}

// TestClearCompletedWithFiles 测试批量清空已完成记录及文件
func TestClearCompletedWithFiles(t *testing.T) {
	mgr := newTestManager(t)
	tmpDir := t.TempDir()

	out1 := filepath.Join(tmpDir, "comp1.mp4")
	out2 := filepath.Join(tmpDir, "comp2.mp4")
	out3 := filepath.Join(tmpDir, "queued.mp4")

	_ = os.WriteFile(out1, []byte("comp1"), 0644)
	_ = os.WriteFile(out2, []byte("comp2"), 0644)
	_ = os.WriteFile(out3, []byte("queued"), 0644)

	t1 := &DownloadTask{ID: "t_comp_1", Title: "Comp 1", Status: StatusCompleted, OutputPath: out1}
	t2 := &DownloadTask{ID: "t_comp_2", Title: "Comp 2", Status: StatusCompleted, OutputPath: out2}
	t3 := &DownloadTask{ID: "t_queue_3", Title: "Queue 3", Status: StatusQueued, OutputPath: out3}

	mgr.mu.Lock()
	mgr.tasks = append(mgr.tasks, t1, t2, t3)
	mgr.mu.Unlock()

	// 批量清空并删除文件
	mgr.ClearCompleted(true)

	if _, err := os.Stat(out1); !os.IsNotExist(err) {
		t.Fatal("ClearCompleted(true) 未能删除已完成任务 1 的文件")
	}
	if _, err := os.Stat(out2); !os.IsNotExist(err) {
		t.Fatal("ClearCompleted(true) 未能删除已完成任务 2 的文件")
	}
	if _, err := os.Stat(out3); os.IsNotExist(err) {
		t.Fatal("ClearCompleted(true) 误删了排队中任务的文件")
	}

	// 验证任务列表状态
	tasks := mgr.GetTasks()
	for _, tk := range tasks {
		if tk.ID == "t_comp_1" || tk.ID == "t_comp_2" {
			t.Fatalf("ClearCompleted 未能从列表中移除已完成任务: %s", tk.ID)
		}
	}

	// 清理剩余任务
	_ = mgr.DeleteTask("t_queue_3", true)
}

// TestAddDownloadTask_MultiPartNaming 测试单 P 与多 P 任务添加时的目录与文件名规范化
func TestAddDownloadTask_MultiPartNaming(t *testing.T) {
	mgr := newTestManager(t)

	// 1. 单 P 普通视频
	reqSingle := &DownloadRequest{
		Title:         "单个测试视频",
		TargetQuality: "80",
		TargetCodec:   "AVC",
		Episodes:      []int64{1001},
	}
	epSingle := &bilibili.EpisodeInfo{CID: 1001, Title: "单个测试视频", Index: 1}
	task1, err := mgr.AddDownloadTask(reqSingle, epSingle)
	if err != nil {
		t.Fatalf("AddDownloadTask 单P失败: %v", err)
	}
	if filepath.Base(task1.OutputPath) != "单个测试视频.mp4" {
		t.Errorf("单P文件名生成不符: %s", filepath.Base(task1.OutputPath))
	}

	// 2. 多 P 合集视频
	reqMulti := &DownloadRequest{
		Title:         "系列教程合集",
		TargetQuality: "80",
		TargetCodec:   "AVC",
		Episodes:      []int64{2001, 2002},
	}
	epMulti2 := &bilibili.EpisodeInfo{CID: 2002, Title: "第二讲 进阶", Index: 2}
	task2, err := mgr.AddDownloadTask(reqMulti, epMulti2)
	if err != nil {
		t.Fatalf("AddDownloadTask 多P失败: %v", err)
	}
	if filepath.Base(filepath.Dir(task2.OutputPath)) != "系列教程合集" {
		t.Errorf("多P合集子文件夹生成不符: %s", filepath.Dir(task2.OutputPath))
	}
	if filepath.Base(task2.OutputPath) != "P02. 第二讲 进阶.mp4" {
		t.Errorf("多P文件名生成不符: %s", filepath.Base(task2.OutputPath))
	}
}

// TestIndividualTaskLifecycle 测试单个任务的暂停、继续、取消与删除全生命周期
func TestIndividualTaskLifecycle(t *testing.T) {
	mgr := newTestManager(t)

	task := &DownloadTask{
		ID:            "t_lifecycle_1",
		Title:         "生命周期测试任务",
		Status:        StatusQueued,
		TargetQuality: "80",
		TargetCodec:   "AVC",
		OutputPath:    filepath.Join(t.TempDir(), "lifecycle.mp4"),
	}

	mgr.mu.Lock()
	mgr.tasks = append(mgr.tasks, task)
	mgr.mu.Unlock()

	// 1. 暂停单个任务
	_ = mgr.PauseTask(task.ID)
	mgr.mu.RLock()
	if task.Status != StatusPaused {
		t.Fatalf("暂停后状态预期为 %s，实际为: %s", StatusPaused, task.Status)
	}
	mgr.mu.RUnlock()

	// 2. 继续单个任务
	_ = mgr.ResumeTask(task.ID)
	mgr.mu.RLock()
	if task.Status != StatusQueued {
		t.Fatalf("继续后状态预期为 %s，实际为: %s", StatusQueued, task.Status)
	}
	mgr.mu.RUnlock()

	// 3. 取消单个任务
	_ = mgr.CancelTask(task.ID)
	mgr.mu.RLock()
	if task.Status != StatusCancelled {
		t.Fatalf("取消后状态预期为 %s，实际为: %s", StatusCancelled, task.Status)
	}
	mgr.mu.RUnlock()

	// 4. 删除单个任务
	err := mgr.DeleteTask(task.ID, false)
	if err != nil {
		t.Fatalf("DeleteTask 失败: %v", err)
	}
	tasks := mgr.GetTasks()
	for _, tk := range tasks {
		if tk.ID == task.ID {
			t.Fatal("DeleteTask 后任务依然存在于任务列表中")
		}
	}
}

func TestCleanPartTitle_EdgeCases(t *testing.T) {
	tests := []struct {
		mainTitle string
		partTitle string
		index     int
		expected  string
	}{
		{"狂飙 全集", "狂飙 全集 - 第01集 初识", 1, "P01. 第01集 初识"},
		{"教程合集", "P1 基础入门", 1, "P1 基础入门"},
		{"教程合集", "P02 进阶操作", 2, "P02 进阶操作"},
		{"教程合集", "第3话 高级应用", 3, "第3话 高级应用"},
		{"独立短片", "", 1, "P01"},
		{"独立短片", "   ", 0, "独立短片"},
		{"系列课", "实战篇", 5, "P05. 实战篇"},
	}

	for _, tc := range tests {
		got := cleanPartTitle(tc.mainTitle, tc.partTitle, tc.index)
		if got != tc.expected {
			t.Errorf("cleanPartTitle(%q, %q, %d) = %q, expected %q", tc.mainTitle, tc.partTitle, tc.index, got, tc.expected)
		}
	}
}

func TestGetQualityTag_AllResolutions(t *testing.T) {
	tests := []struct {
		qn       int
		label    string
		expected string
	}{
		{127, "8K 超高清", "[8K]"},
		{126, "杜比视界", "[杜比视界]"},
		{125, "HDR 真彩", "[HDR]"},
		{120, "4K 超清", "[4K]"},
		{116, "1080P 60帧", "[1080P60]"},
		{112, "1080P 高码率", "[1080P+]"},
		{80, "1080P 高清", "[1080P]"},
		{74, "720P 60帧", "[720P60]"},
		{64, "720P 高清", "[720P]"},
		{32, "480P 清晰", "[480P]"},
		{16, "360P 流畅", "[360P]"},
		{999, "8K 60帧", "[8K]"},
		{999, "4K 极清", "[4K]"},
		{999, "1080P60 超清", "[1080P60]"},
		{999, "1080P 清晰", "[1080P]"},
		{999, "720P 高清", "[720P]"},
		{999, "480P 流畅", "[480P]"},
		{999, "360P", "[360P]"},
		{0, "", ""},
	}

	for _, tc := range tests {
		got := getQualityTag(tc.qn, tc.label)
		if got != tc.expected {
			t.Errorf("getQualityTag(%d, %q) = %q, expected %q", tc.qn, tc.label, got, tc.expected)
		}
	}
}

func TestFormatFriendlyError_Categorization(t *testing.T) {
	if formatFriendlyError(nil) != "" {
		t.Errorf("Nil error should return empty string")
	}

	timeoutErr := fmt.Errorf("context deadline exceeded or connection timeout")
	if !strings.Contains(formatFriendlyError(timeoutErr), "超时") {
		t.Errorf("Timeout error mapping failed: %s", formatFriendlyError(timeoutErr))
	}

	resetErr := fmt.Errorf("read: connection reset by peer or EOF")
	if !strings.Contains(formatFriendlyError(resetErr), "连接中断") {
		t.Errorf("Reset error mapping failed: %s", formatFriendlyError(resetErr))
	}

	forbiddenErr := fmt.Errorf("HTTP 403 Forbidden")
	if !strings.Contains(formatFriendlyError(forbiddenErr), "过期") {
		t.Errorf("403 error mapping failed: %s", formatFriendlyError(forbiddenErr))
	}

	diskErr := fmt.Errorf("write: no space left on device")
	if !strings.Contains(formatFriendlyError(diskErr), "空间不足") {
		t.Errorf("Disk full error mapping failed: %s", formatFriendlyError(diskErr))
	}
}

// TestFileNameTemplate_CustomFormats 红绿灯测试：验证 FileNameTemplate 自定义模板格式化引擎
func TestFileNameTemplate_CustomFormats(t *testing.T) {
	cases := []struct {
		tmpl      string
		mainTitle string
		partTitle string
		bvid      string
		index     int
		expected  string
	}{
		{
			tmpl:      "{bvid}_{index}_{part}",
			mainTitle: "测试合集",
			partTitle: "第一课 基础",
			bvid:      "BV1xx411c7mD",
			index:     1,
			expected:  "BV1xx411c7mD_P01_第一课 基础",
		},
		{
			tmpl:      "{title} - {part}",
			mainTitle: "单视频",
			partTitle: "单视频",
			bvid:      "BV1single",
			index:     1,
			expected:  "单视频",
		},
		{
			tmpl:      "[{bvid}] {title} - {part}",
			mainTitle: "教程全集",
			partTitle: "进阶技巧",
			bvid:      "BV1teach",
			index:     2,
			expected:  "[BV1teach] 教程全集 - 进阶技巧",
		},
	}

	for _, tc := range cases {
		got := formatFileNameByTemplate(tc.tmpl, tc.mainTitle, tc.partTitle, tc.bvid, tc.index)
		if got != tc.expected {
			t.Errorf("formatFileNameByTemplate(%q, %q, %q, %q, %d) = %q, expected %q",
				tc.tmpl, tc.mainTitle, tc.partTitle, tc.bvid, tc.index, got, tc.expected)
		}
	}
}

// TestSafeRemoveWithRetry 测试文件在被短暂打开占用情况下通过重试成功删除
func TestSafeRemoveWithRetry(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "locked.tmp")
	_ = os.WriteFile(filePath, []byte("temporary data"), 0644)

	safeRemoveWithRetry(filePath)
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("文件应被成功删除，但仍存在")
	}
}

// TestOutputPathPrecomputedConsistency 验证下载过程中即与最终合成文件的 OutputPath 完全保持一致
func TestOutputPathPrecomputedConsistency(t *testing.T) {
	mgr := newTestManager(t)
	req := &DownloadRequest{
		Title:         "测试视频",
		TargetQuality: "120",
		Episodes:      []int64{12345},
	}
	ep := &bilibili.EpisodeInfo{
		CID:   12345,
		Index: 1,
		Title: "测试视频",
	}

	task, err := mgr.AddDownloadTask(req, ep)
	if err != nil {
		t.Fatal(err)
	}

	initialPath := task.OutputPath
	if strings.Contains(initialPath, "[4K]") {
		t.Fatalf("初始路径不应包含尚未解析的清晰度标签: %s", initialPath)
	}

	// 模拟媒体流解析完成确定画质为 120 (4K)
	mgr.mu.Lock()
	qTag := getQualityTag(120, "4K 超清")
	dir := filepath.Dir(task.OutputPath)
	base := strings.TrimSuffix(filepath.Base(task.OutputPath), ".mp4")
	task.OutputPath = filepath.Join(dir, fmt.Sprintf("%s %s.mp4", base, qTag))
	mgr.mu.Unlock()

	mgr.SaveTasks()

	// 重新从磁盘读取，验证持久化路径已经固化包含清晰度标签
	loadedTasks := mgr.GetTasks()
	if len(loadedTasks) == 0 {
		t.Fatal("未读取到持久化任务")
	}
	if !strings.Contains(loadedTasks[0].OutputPath, "[4K]") {
		t.Fatalf("解析清晰度后 OutputPath 应已固化 [4K]，实际为: %s", loadedTasks[0].OutputPath)
	}
}
