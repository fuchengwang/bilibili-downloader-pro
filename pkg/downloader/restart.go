package downloader

import (
	"context"
	"errors"
)

// PrepareForRestart uses the same lock as entering the merge stage. It either
// refuses a live merge or invalidates workers before they can begin one, saves
// the paused tasks, and waits for download file handles to be released.
func (m *DownloadManager) PrepareForRestart(ctx context.Context) error {
	m.mu.Lock()
	for _, task := range m.tasks {
		if task != nil && task.Status == StatusMerging {
			m.mu.Unlock()
			return errors.New("视频正在合并，请完成后再重启更新")
		}
	}
	var done []<-chan struct{}
	for id, worker := range m.workers {
		if worker.cancel != nil {
			worker.cancel()
		}
		worker.invalidated = true
		m.workers[id] = worker
		if worker.done != nil {
			done = append(done, worker.done)
		}
	}
	var modified []*DownloadTask
	for _, task := range m.tasks {
		if task != nil && (task.Status == StatusDownloading || task.Status == StatusQueued) {
			task.Status = StatusPaused
			task.Speed = 0
			task.SpeedStr = "0 KB/s"
			task.ETAStr = "--"
			modified = append(modified, task)
		}
	}
	m.mu.Unlock()
	if err := m.SaveTasks(); err != nil {
		return errors.New("下载任务未能保存，请稍后再重启")
	}
	for _, task := range modified {
		m.notifyChange(task)
	}
	for _, closed := range done {
		select {
		case <-closed:
		case <-ctx.Done():
			return errors.New("下载任务正在暂停，请稍后再重启")
		}
	}
	return nil
}
