package downloader

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func progressFixture(t *testing.T, ctx context.Context, hasAudio bool) (*DownloadManager, *DownloadTask, *taskDownloadProgress) {
	t.Helper()
	m := newTestManager(t)
	task := &DownloadTask{ID: "progress-fixture", Status: StatusDownloading}
	m.tasks = append(m.tasks, task)
	m.workers[task.ID] = workerHandle{token: "worker-1"}
	p := newTaskDownloadProgress(ctx, m, task, "worker-1", 4*1024*1024*1024, 0, hasAudio)
	return m, task, p
}

func TestTaskProgressSeparatesRestoredBytesFromSpeed(t *testing.T) {
	_, task, p := progressFixture(t, context.Background(), false)
	start := p.lastTime
	// Reconstruct a large movie's already saved parts without transferring data.
	p.record(1024*1024*1024, false)
	if !p.tick(start.Add(time.Second)) {
		t.Fatal("active worker ignored")
	}
	if task.Progress != 25 || task.Speed != 0 || p.received != 0 {
		t.Fatalf("restored bytes inflated speed: progress %.2f, speed %d, network %d", task.Progress, task.Speed, p.received)
	}
	p.record(128*1024, true)
	p.tick(start.Add(2 * time.Second))
	wantSpeed := int64(39321) // 128 KiB/s * EMA weight 0.3, rounded down.
	if task.Speed != wantSpeed {
		t.Fatalf("speed %d, want %d from actual network bytes", task.Speed, wantSpeed)
	}
	if task.DownloadedBytes != 1024*1024*1024+128*1024 {
		t.Fatalf("bytes %d", task.DownloadedBytes)
	}
	// Rewinding discarded data must change completion, never produce negative speed.
	p.record(-1024*1024*1024, true)
	p.tick(start.Add(3 * time.Second))
	if task.Speed < 0 || p.received != 128*1024 {
		t.Fatalf("rewind changed network accounting: %+v", p)
	}
}

func TestTaskProgressRejectsStaleWorkersAndPausedTasks(t *testing.T) {
	for _, scenario := range []string{"paused", "queued", "merging", "completed", "invalidated", "replaced", "cancelled context"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m, task, p := progressFixture(t, ctx, false)
			m.mu.Lock()
			switch scenario {
			case "paused":
				task.Status = StatusPaused
			case "queued":
				task.Status = StatusQueued
			case "merging":
				task.Status = StatusMerging
			case "completed":
				task.Status, task.Progress, task.SpeedStr = StatusCompleted, 100, "已完成"
			case "invalidated":
				m.workers[task.ID] = workerHandle{token: p.token, invalidated: true}
			case "replaced":
				m.workers[task.ID] = workerHandle{token: "worker-2"}
			case "cancelled context":
				cancel()
			}
			before := *task
			m.mu.Unlock()
			p.record(1024*1024, true)
			p.record(512*1024*1024, false)
			p.setSize(true, 16*1024*1024)
			if p.tick(p.lastTime.Add(time.Second)) {
				t.Fatal("stale reporter kept running")
			}
			if !reflect.DeepEqual(*task, before) {
				t.Fatalf("stale callback modified task:\nbefore %+v\nafter %+v", before, *task)
			}
		})
	}
}

func TestTaskProgressUpdatesPreviouslyUnknownTotal(t *testing.T) {
	_, task, p := progressFixture(t, context.Background(), true)
	p.record(512*1024, true)
	p.tick(p.lastTime.Add(time.Second))
	if task.TotalBytes != 0 || task.ETAStr != "--" {
		t.Fatal("unknown audio size created a misleading percent or ETA")
	}
	p.setSize(true, 1024*1024)
	p.setSize(false, 1024*1024)
	if task.TotalBytes != 2*1024*1024 || task.Progress != 25 {
		t.Fatalf("late total ignored: %+v", task)
	}
}

func TestTaskProgressStopJoinsBeforeMerging(t *testing.T) {
	_, task, p := progressFixture(t, context.Background(), false)
	stop := p.start()
	p.record(128*1024, true)
	stop()
	stop() // runTask stops explicitly before merging, then through its defer.
	task.Status, task.Progress, task.SpeedStr = StatusCompleted, 100, "已完成"
	if p.tick(time.Now().Add(time.Second)) {
		t.Fatal("completed task still reports download ticks")
	}
	if task.Progress != 100 || task.SpeedStr != "已完成" {
		t.Fatal("reporter overwrote completion")
	}
}
