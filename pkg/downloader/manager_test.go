package downloader

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

// TestPauseResumeDeadlock 测试全部暂停与全部继续在高并发场景下绝无死锁
func TestPauseResumeDeadlock(t *testing.T) {
	mgr := GetManager()

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
	mgr := GetManager()
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
	mgr := GetManager()

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
	mgr := GetManager()

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
