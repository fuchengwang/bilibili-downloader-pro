package downloader

import (
	"context"
	"fmt"
	"os"
	"time"

	"bilibili_downloader/pkg/utils"
)

// All fields are protected by manager.mu. Completion counts include resumable
// bytes on disk; speed uses only bytes transferred during this worker's run.
type taskDownloadProgress struct {
	manager                *DownloadManager
	ctx                    context.Context
	task                   *DownloadTask
	token                  string
	videoSize, audioSize   int64
	hasAudio               bool
	received, lastReceived int64
	lastTime               time.Time
	speed                  int64
}

func newTaskDownloadProgress(ctx context.Context, m *DownloadManager, task *DownloadTask, token string, videoSize, audioSize int64, hasAudio bool) *taskDownloadProgress {
	p := &taskDownloadProgress{manager: m, ctx: ctx, task: task, token: token, videoSize: videoSize, audioSize: audioSize, hasAudio: hasAudio, lastTime: time.Now()}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.activeLocked() {
		task.DownloadedBytes = existingStreamSize(task.VideoTmpPath)
		if hasAudio {
			task.DownloadedBytes += existingStreamSize(task.AudioTmpPath)
		}
		p.updateDisplayLocked()
	}
	return p
}

func existingStreamSize(path string) int64 {
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return info.Size()
	}
	return 0
}

func (p *taskDownloadProgress) activeLocked() bool {
	w, exists := p.manager.workers[p.task.ID]
	return p.ctx.Err() == nil && exists && !w.invalidated && w.token == p.token && p.task.Status == StatusDownloading
}

func (p *taskDownloadProgress) record(delta int64, fromNetwork bool) {
	p.manager.mu.Lock()
	defer p.manager.mu.Unlock()
	if !p.activeLocked() {
		return
	}
	p.task.DownloadedBytes += delta
	if p.task.DownloadedBytes < 0 {
		p.task.DownloadedBytes = 0
	}
	if fromNetwork && delta > 0 {
		p.received += delta
	}
}

func (p *taskDownloadProgress) setSize(video bool, size int64) {
	p.manager.mu.Lock()
	defer p.manager.mu.Unlock()
	if !p.activeLocked() {
		return
	}
	if video {
		p.videoSize = size
	} else {
		p.audioSize = size
	}
	p.updateDisplayLocked()
}

func (p *taskDownloadProgress) updateDisplayLocked() {
	t := p.task
	// A missing audio/video length must not make an incomplete task look 100% done.
	t.TotalBytes = 0
	if p.videoSize > 0 && (!p.hasAudio || p.audioSize > 0) {
		t.TotalBytes = p.videoSize + p.audioSize
	}
	t.Speed, t.SpeedStr = p.speed, utils.FormatSpeed(p.speed)
	t.Progress, t.ETAStr = 0, "--"
	if t.TotalBytes <= 0 {
		t.SizeStr = fmt.Sprintf("已下载 %s / 总大小待确认", utils.FormatBytes(t.DownloadedBytes))
		return
	}
	t.Progress = float64(t.DownloadedBytes) * 100 / float64(t.TotalBytes)
	if t.Progress > 100 {
		t.Progress = 100
	}
	t.SizeStr = fmt.Sprintf("%s / %s", utils.FormatBytes(t.DownloadedBytes), utils.FormatBytes(t.TotalBytes))
	remaining := t.TotalBytes - t.DownloadedBytes
	if remaining <= 0 {
		t.ETAStr = "完成中"
	} else if p.speed > 0 {
		t.ETAStr = utils.FormatDuration(int(remaining / p.speed))
	}
}

func (p *taskDownloadProgress) tick(now time.Time) bool {
	p.manager.mu.Lock()
	if !p.activeLocked() {
		p.manager.mu.Unlock()
		return false
	}
	elapsed := now.Sub(p.lastTime).Seconds()
	if elapsed > 0 {
		instant := float64(p.received-p.lastReceived) / elapsed
		p.speed = int64(float64(p.speed)*0.7 + instant*0.3)
		p.lastTime, p.lastReceived = now, p.received
	}
	p.updateDisplayLocked()
	p.manager.mu.Unlock()
	p.manager.notifyChange(p.task)
	return true
}

// Join the reporter before entering merging or releasing the worker handle.
// A final queued tick must never overwrite paused/completed or a newer worker.
func (p *taskDownloadProgress) start() func() {
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-p.ctx.Done():
				return
			case now := <-ticker.C:
				if !p.tick(now) {
					return
				}
			}
		}
	}()
	return func() {
		select {
		case <-done:
		default:
			close(done)
		}
		<-stopped
	}
}
