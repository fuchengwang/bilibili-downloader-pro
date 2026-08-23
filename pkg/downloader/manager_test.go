package downloader

import (
	"fmt"
	"os"
	"path/filepath"
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
	mgr.activeCount = 1
	mgr.mu.Unlock()

	// 模拟新 worker (token_v2) 已经拉起
	mgr.mu.Lock()
	mgr.workers[taskID] = workerHandle{
		token: "token_v2",
		cancel: func() {
			// cancel v2
		},
	}
	mgr.activeCount = 2
	mgr.mu.Unlock()

	// 旧 worker (token_v1) 退出并执行清理逻辑
	mgr.mu.Lock()
	if w, ok := mgr.workers[taskID]; ok && w.token == "token_v1" {
		delete(mgr.workers, taskID)
		mgr.activeCount--
	}
	mgr.mu.Unlock()

	// 验证 token_v2 未被旧 worker 误删
	mgr.mu.RLock()
	w, exists := mgr.workers[taskID]
	active := mgr.activeCount
	mgr.mu.RUnlock()

	if !exists || w.token != "token_v2" {
		t.Fatalf("新 Worker Token 被旧 Worker 错误覆盖或删除了: exists=%v, token=%s", exists, w.token)
	}
	if active != 2 {
		t.Fatalf("Active count 计算异常: %d", active)
	}

	// 清理模拟状态
	mgr.mu.Lock()
	delete(mgr.workers, taskID)
	mgr.activeCount = 0
	mgr.mu.Unlock()
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
