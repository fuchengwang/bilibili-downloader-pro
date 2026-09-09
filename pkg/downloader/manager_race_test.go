package downloader

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bilibili_downloader/pkg/config"
)

// TestManagerNotifyDataRace 高并发模拟下载进度更新、外部查询 GetTasks 与回调 notifyChange，确保 0 数据竞争
func TestManagerNotifyDataRace(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.NewConfigManager(tmpDir)
	mgr := NewDownloadManager(cfg)

	var callbackInvoked int64
	mgr.SetCallback(func(task *DownloadTask) {
		// 校验回调接收到的副本不为 nil 且能安全读取所有字段
		if task != nil && len(task.ID) > 0 {
			atomic.AddInt64(&callbackInvoked, 1)
			_ = task.SpeedStr
			_ = task.DownloadedBytes
			_ = task.Progress
		}
	})

	// 初始化 5 个模拟并发任务
	for i := 0; i < 5; i++ {
		task := &DownloadTask{
			ID:           fmt.Sprintf("race_task_%d", i),
			Title:        fmt.Sprintf("Race Task %d", i),
			Status:       StatusDownloading,
			TotalBytes:   1024 * 1024 * 100,
			VideoTmpPath: filepath.Join(tmpDir, fmt.Sprintf("v_%d.tmp", i)),
			AudioTmpPath: filepath.Join(tmpDir, fmt.Sprintf("a_%d.tmp", i)),
		}
		mgr.mu.Lock()
		mgr.tasks = append(mgr.tasks, task)
		mgr.mu.Unlock()
	}

	var wg sync.WaitGroup
	stopChan := make(chan struct{})

	// 1. 模拟 4 个下载 worker 高频并发更新 DownloadedBytes
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for {
				select {
				case <-stopChan:
					return
				default:
					mgr.mu.Lock()
					for _, task := range mgr.tasks {
						task.DownloadedBytes += 1024
						if task.DownloadedBytes > task.TotalBytes {
							task.DownloadedBytes = 0
						}
					}
					mgr.mu.Unlock()
					time.Sleep(1 * time.Millisecond)
				}
			}
		}(i)
	}

	// 2. 模拟高频 progressTicker 更新状态并触发 notifyChange
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopChan:
					return
				default:
					mgr.mu.RLock()
					tasks := append([]*DownloadTask(nil), mgr.tasks...)
					mgr.mu.RUnlock()

					for _, task := range tasks {
						mgr.mu.Lock()
						task.Speed = 5 * 1024 * 1024
						task.SpeedStr = "5.0 MB/s"
						task.Progress = 45.5
						task.ETAStr = "01:23"
						mgr.mu.Unlock()

						mgr.notifyChange(task)
					}
					time.Sleep(2 * time.Millisecond)
				}
			}
		}()
	}

	// 3. 模拟前端 Wails 频繁调用 GetTasks() 序列化快照
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopChan:
					return
				default:
					tasks := mgr.GetTasks()
					for _, t := range tasks {
						_ = t.SpeedStr
						_ = t.DownloadedBytes
						_ = t.Status
					}
					time.Sleep(3 * time.Millisecond)
				}
			}
		}()
	}

	// 4. 模拟定时持久化 SaveTasks()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopChan:
				return
			default:
				mgr.SaveTasks()
				time.Sleep(5 * time.Millisecond)
			}
		}
	}()

	// 运行高压并发 600ms
	time.Sleep(600 * time.Millisecond)
	close(stopChan)
	wg.Wait()

	if atomic.LoadInt64(&callbackInvoked) == 0 {
		t.Fatal("未收到任何回调通知")
	}
}
