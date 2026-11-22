package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
	"bilibili_downloader/pkg/config"
)

func queueBatch(t *testing.T, m *DownloadManager, name string, first int64, count int) []*DownloadTask {
	t.Helper()
	req := &DownloadRequest{Title: name, IsBangumi: true, TargetQuality: "80", TargetCodec: "AVC"}
	var episodes []bilibili.EpisodeInfo
	for i := 0; i < count; i++ {
		episodes = append(episodes, bilibili.EpisodeInfo{CID: first + int64(i), EPID: first + int64(i), Index: i + 1, Title: fmt.Sprintf("%s%d", name, i+1)})
	}
	tasks, err := m.AddDownloadTasks(req, episodes)
	if err != nil {
		t.Fatal(err)
	}
	return tasks
}

func queueTitles(m *DownloadManager) []string {
	tasks := m.GetTasks()
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].QueueOrder < tasks[j].QueueOrder })
	var titles []string
	for _, task := range tasks {
		titles = append(titles, task.PartTitle)
	}
	return titles
}

// Cancel synchronously in the start callback, before the worker may issue HTTP.
func captureQueueStarts(t *testing.T, m *DownloadManager) []string {
	t.Helper()
	var started []string
	var done []<-chan struct{}
	m.SetCallback(func(task *DownloadTask) {
		if task.Status != StatusDownloading {
			return
		}
		m.mu.Lock()
		w := m.workers[task.ID]
		started = append(started, task.PartTitle)
		done = append(done, w.done)
		w.cancel()
		m.mu.Unlock()
	})
	m.checkAndSpawnTasks()
	for _, ch := range done {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("cancelled worker did not exit")
		}
	}
	m.SetCallback(nil)
	return started
}

func TestQueueFIFOAcrossBatchesAndImmediateSlotFilling(t *testing.T) {
	m := newTestManager(t)
	queueBatch(t, m, "A", 10, 3)
	queueBatch(t, m, "B", 20, 2)
	want := []string{"A1", "A2", "A3", "B1", "B2"}
	if got := queueTitles(m); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if got := captureQueueStarts(t, m); !reflect.DeepEqual(got, want[:3]) {
		t.Fatal("one scheduler pass did not fill the slots in order", got)
	}
	m.mu.Lock()
	for _, task := range m.tasks {
		if task.Status == StatusDownloading {
			task.Status = StatusCompleted
		}
	}
	m.mu.Unlock()
	if got := captureQueueStarts(t, m); !reflect.DeepEqual(got, want[3:]) {
		t.Fatal(got)
	}
}

func TestQueuePauseAndResumeKeepPlaceAndWaitForOldWorker(t *testing.T) {
	m := newTestManager(t)
	a := queueBatch(t, m, "A", 10, 3)
	queueBatch(t, m, "B", 20, 2)
	if err := m.PauseTask(a[1].ID); err != nil {
		t.Fatal(err)
	}
	if got := captureQueueStarts(t, m); !reflect.DeepEqual(got, []string{"A1", "A3", "B1"}) {
		t.Fatal(got)
	}
	if err := m.ResumeTask(a[1].ID); err != nil {
		t.Fatal(err)
	}
	// Keep an old cancelled worker's file handles reserved until it exits.
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	m.mu.Lock()
	for _, task := range m.tasks {
		if task.Status == StatusDownloading {
			task.Status = StatusCompleted
		}
	}
	m.workers[a[1].ID] = workerHandle{token: "old", cancel: cancel, done: done, invalidated: true}
	m.mu.Unlock()
	if got := captureQueueStarts(t, m); !reflect.DeepEqual(got, []string{"B2"}) {
		t.Fatal("old worker was restarted before releasing files", got)
	}
	m.mu.Lock()
	delete(m.workers, a[1].ID)
	close(done)
	m.mu.Unlock()
	if got := captureQueueStarts(t, m); !reflect.DeepEqual(got, []string{"A2"}) {
		t.Fatal("resumed task lost its place", got)
	}
}

func TestQueueBatchIsDurableBeforeAnyNotificationAndDeduplicates(t *testing.T) {
	m := newTestManager(t)
	callbacks := 0
	m.SetCallback(func(task *DownloadTask) {
		callbacks++
		data, err := os.ReadFile(m.cfgMgr.GetTasksPath())
		var saved []*DownloadTask
		if err != nil || json.Unmarshal(data, &saved) != nil || len(saved) != 100 {
			t.Error("part of batch published before commit", len(saved), err)
		}
	})
	a := queueBatch(t, m, "A", 100, 100)
	if callbacks != 100 {
		t.Fatal(callbacks)
	}
	m.SetCallback(nil)
	a[0].Status = StatusPaused
	a[0].ResumeOnStartup = true
	beforeOrder, beforeCache := a[0].QueueOrder, a[0].VideoTmpPath
	req := &DownloadRequest{Title: "A", IsBangumi: true, TargetQuality: "80", TargetCodec: "AVC"}
	result, err := m.AddDownloadTasks(req, []bilibili.EpisodeInfo{{CID: 100}, {CID: 100}})
	if err != nil || len(result) != 1 || result[0].ID != a[0].ID || result[0].Status != StatusQueued || result[0].ResumeOnStartup {
		t.Fatal("duplicate/retry failed", err)
	}
	if result[0].QueueOrder != beforeOrder || result[0].VideoTmpPath != beforeCache || len(m.GetTasks()) != 100 {
		t.Fatal("retry changed queue identity or position")
	}
}

func TestQueueBatchPersistenceFailureRollsBackAllChanges(t *testing.T) {
	m := newTestManager(t)
	a := queueBatch(t, m, "A", 10, 1)[0]
	a.Status = StatusPaused
	order := a.QueueOrder
	if err := os.Remove(m.cfgMgr.GetTasksPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(m.cfgMgr.GetTasksPath(), 0700); err != nil {
		t.Fatal(err)
	}
	callbacks := 0
	m.SetCallback(func(*DownloadTask) { callbacks++ })
	req := &DownloadRequest{Title: "A", IsBangumi: true, TargetQuality: "80", TargetCodec: "AVC"}
	result, err := m.AddDownloadTasks(req, []bilibili.EpisodeInfo{{CID: 10}, {CID: 11, Index: 2, Title: "A2"}})
	if err == nil || result != nil || callbacks != 0 {
		t.Fatal("failed batch was accepted or published", err)
	}
	if len(m.GetTasks()) != 1 || a.Status != StatusPaused || a.QueueOrder != order {
		t.Fatal("failed batch did not restore the original queue")
	}
}

func TestQueueBatchPreparationFailureAdmitsNothing(t *testing.T) {
	m := newTestManager(t)
	queueBatch(t, m, "old", 1, 1)
	before, err := os.ReadFile(m.cfgMgr.GetTasksPath())
	if err != nil {
		t.Fatal(err)
	}
	root := m.cfgMgr.Get().DownloadDir
	parent := filepath.Join(root, "collection")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(parent, "blocked")
	if err := os.WriteFile(blocked, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	m.directoryRegistry = map[string]string{pathKey(root) + "|ugc:5": parent, pathKey(parent) + "|video:BVblocked": blocked}
	req := &DownloadRequest{Title: "new", CollectionID: 5, IsCollection: true}
	_, err = m.AddDownloadTasks(req, []bilibili.EpisodeInfo{{CID: 2, Title: "first", PartCount: 1}, {CID: 3, BVID: "BVblocked", Title: "second", PartCount: 2}})
	if err == nil || len(m.GetTasks()) != 1 {
		t.Fatal("part of failed preparation entered queue", err)
	}
	after, _ := os.ReadFile(m.cfgMgr.GetTasksPath())
	if !bytes.Equal(before, after) {
		t.Fatal("failed preparation changed persisted queue")
	}
}

func TestQueueLegacyOrderAndRestartIntentArePersistedOnce(t *testing.T) {
	m := newTestManager(t)
	m.tasks = []*DownloadTask{
		{ID: newUUID(), PartTitle: "manual-pause", Status: StatusPaused, CreatedAt: 1},
		{ID: newUUID(), PartTitle: "update-pause", Status: StatusPaused, ResumeOnStartup: true, CreatedAt: 1},
		{ID: newUUID(), PartTitle: "queued", Status: StatusQueued, CreatedAt: 1},
		{ID: newUUID(), PartTitle: "merging", Status: StatusMerging, CreatedAt: 1},
		{ID: newUUID(), PartTitle: "downloading", Status: StatusDownloading, CreatedAt: 1},
	}
	if err := m.SaveTasks(); err != nil {
		t.Fatal(err)
	}
	restored := NewDownloadManager(m.cfgMgr)
	want := []string{"downloading", "merging", "queued", "update-pause", "manual-pause"}
	if got := queueTitles(restored); !reflect.DeepEqual(got, want) {
		t.Fatal("legacy order was not recovered", got)
	}
	for _, task := range restored.GetTasks() {
		status := StatusQueued
		if task.PartTitle == "manual-pause" {
			status = StatusPaused
		}
		if task.Status != status || task.ResumeOnStartup {
			t.Fatal("restart changed user pause intent", task.PartTitle, task.Status)
		}
	}
	if got := queueTitles(NewDownloadManager(m.cfgMgr)); !reflect.DeepEqual(got, want) {
		t.Fatal("migration changed order on second launch", got)
	}
}

func TestQueuePauseAllInvalidatesWorkersAndClearsRestartIntent(t *testing.T) {
	m := newTestManager(t)
	a := queueBatch(t, m, "A", 10, 2)
	m.workers[a[0].ID] = workerHandle{token: "running", cancel: func() {}}
	a[0].Status = StatusDownloading
	a[1].Status = StatusPaused
	a[1].ResumeOnStartup = true
	if err := m.PauseAll(); err != nil {
		t.Fatal(err)
	}
	if !m.workers[a[0].ID].invalidated {
		t.Fatal("old worker was not invalidated")
	}
	for _, task := range NewDownloadManager(m.cfgMgr).GetTasks() {
		if task.Status != StatusPaused || task.ResumeOnStartup {
			t.Fatal("explicit pause was not retained")
		}
	}
}

func TestQueueResumePersistenceFailureKeepsTasksPaused(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			m := newTestManager(t)
			a := queueBatch(t, m, "A", 10, 2)
			if err := m.PauseAll(); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(m.cfgMgr.GetTasksPath()); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(m.cfgMgr.GetTasksPath(), 0700); err != nil {
				t.Fatal(err)
			}
			err := m.ResumeTask(a[0].ID)
			if all {
				err = m.ResumeAll()
			}
			if err == nil {
				t.Fatal("resume accepted without saving")
			}
			for _, task := range m.GetTasks() {
				if task.Status != StatusPaused {
					t.Fatal("unsaved resumed task can run")
				}
			}
		})
	}
}

func TestQueueNotificationCanReenterBatchAdmission(t *testing.T) {
	m := newTestManager(t)
	entered := false
	m.SetCallback(func(*DownloadTask) {
		if entered {
			return
		}
		entered = true
		_, err := m.AddDownloadTask(&DownloadRequest{Title: "B", IsBangumi: true}, &bilibili.EpisodeInfo{CID: 99, Title: "B1"})
		if err != nil {
			t.Error(err)
		}
	})
	done := make(chan struct{})
	go func() { defer close(done); queueBatch(t, m, "A", 10, 2) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("admission held a lock during callbacks")
	}
	if got := queueTitles(m); !reflect.DeepEqual(got, []string{"A1", "A2", "B1"}) {
		t.Fatal(got)
	}
}

func TestQueueCompletedDuplicateDoesNotEmitAnotherCompletion(t *testing.T) {
	m := newTestManager(t)
	a := queueBatch(t, m, "A", 10, 1)[0]
	a.Status = StatusCompleted
	writeStorageFixture(t, a.OutputPath, []byte("finished"))
	notifications := 0
	m.SetCallback(func(*DownloadTask) { notifications++ })
	req := &DownloadRequest{Title: "A", IsBangumi: true, TargetQuality: "80", TargetCodec: "AVC"}
	result, err := m.AddDownloadTasks(req, []bilibili.EpisodeInfo{{CID: 10}})
	if err != nil || len(result) != 1 || result[0].ID != a.ID || notifications != 0 {
		t.Fatal("existing file created a duplicate completion", err, notifications)
	}
}

func TestQueueReorderPersistsAndPreemptsOverflowDownloading(t *testing.T) {
	m := newTestManager(t)
	cfg := m.cfgMgr.Get()
	cfg.MaxConcurrent = 2
	if err := m.cfgMgr.Save(cfg); err != nil {
		t.Fatal(err)
	}

	a := queueBatch(t, m, "A", 10, 4)
	cancelled1, cancelled2 := false, false
	m.mu.Lock()
	a[0].Status = StatusDownloading
	a[1].Status = StatusDownloading
	m.workers[a[0].ID] = workerHandle{token: "w1", cancel: func() { cancelled1 = true }}
	m.workers[a[1].ID] = workerHandle{token: "w2", cancel: func() { cancelled2 = true }}
	m.mu.Unlock()

	if err := m.ReorderTasks([]string{a[3].ID, a[0].ID, a[1].ID, a[2].ID}); err != nil {
		t.Fatal(err)
	}

	wantOrder := []string{"A4", "A1", "A2", "A3"}
	if got := queueTitles(m); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("unexpected queue order: %v", got)
	}
	if got := queueTitles(NewDownloadManager(m.cfgMgr)); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("reloaded queue order mismatch: %v", got)
	}

	m.mu.RLock()
	w1 := m.workers[a[0].ID]
	w2 := m.workers[a[1].ID]
	s1 := a[0].Status
	s2 := a[1].Status
	m.mu.RUnlock()

	if s1 != StatusDownloading || cancelled1 || w1.invalidated {
		t.Fatalf("A1 should keep downloading untouched: status=%s cancelled=%v invalidated=%v", s1, cancelled1, w1.invalidated)
	}
	if s2 != StatusQueued || !cancelled2 || !w2.invalidated {
		t.Fatalf("A2 should be softly preempted back to queued: status=%s cancelled=%v invalidated=%v", s2, cancelled2, w2.invalidated)
	}

	// Even before the invalidated worker finishes exiting, the vacated slot immediately starts A4.
	if got := captureQueueStarts(t, m); !reflect.DeepEqual(got, []string{"A4"}) {
		t.Fatalf("expected prioritized A4 to spawn immediately, got %v", got)
	}
}

func TestQueuePrioritizeResumesPausedAndErrorAndNeverPreemptsMerging(t *testing.T) {
	m := newTestManager(t)
	cfg := m.cfgMgr.Get()
	cfg.MaxConcurrent = 2
	if err := m.cfgMgr.Save(cfg); err != nil {
		t.Fatal(err)
	}

	a := queueBatch(t, m, "A", 10, 4)
	cancelledMerging, cancelledDownloading := false, false
	m.mu.Lock()
	a[0].Status = StatusMerging
	a[1].Status = StatusDownloading
	a[2].Status = StatusPaused
	a[3].Status = StatusError
	a[3].ErrorMsg = "boom"
	m.workers[a[0].ID] = workerHandle{token: "w1", cancel: func() { cancelledMerging = true }}
	m.workers[a[1].ID] = workerHandle{token: "w2", cancel: func() { cancelledDownloading = true }}
	m.mu.Unlock()

	// Passing [A4, A3] must preserve their existing relative order (A3 before A4) at the front.
	if err := m.PrioritizeTasks([]string{a[3].ID, a[2].ID}); err != nil {
		t.Fatal(err)
	}

	wantOrder := []string{"A3", "A4", "A1", "A2"}
	if got := queueTitles(m); !reflect.DeepEqual(got, wantOrder) {
		t.Fatalf("unexpected prioritized order: %v", got)
	}

	m.mu.RLock()
	s1, s2, s3, s4 := a[0].Status, a[1].Status, a[2].Status, a[3].Status
	err4 := a[3].ErrorMsg
	w1 := m.workers[a[0].ID]
	w2 := m.workers[a[1].ID]
	m.mu.RUnlock()

	if s3 != StatusQueued || s4 != StatusQueued || err4 != "" {
		t.Fatalf("prioritized tasks should resume cleanly: A3=%s A4=%s err4=%q", s3, s4, err4)
	}
	if s1 != StatusMerging || cancelledMerging || w1.invalidated {
		t.Fatalf("merging task A1 must never be preempted: status=%s cancelled=%v invalidated=%v", s1, cancelledMerging, w1.invalidated)
	}
	if s2 != StatusQueued || !cancelledDownloading || !w2.invalidated {
		t.Fatalf("downloading task A2 should be preempted: status=%s cancelled=%v invalidated=%v", s2, cancelledDownloading, w2.invalidated)
	}
}

func TestQueueReorderPersistenceFailureRollsBackWithoutCancellingWorkers(t *testing.T) {
	m := newTestManager(t)
	cfg := m.cfgMgr.Get()
	cfg.MaxConcurrent = 2
	if err := m.cfgMgr.Save(cfg); err != nil {
		t.Fatal(err)
	}

	a := queueBatch(t, m, "A", 10, 3)
	cancelled1, cancelled2 := false, false
	m.mu.Lock()
	a[0].Status = StatusDownloading
	a[1].Status = StatusDownloading
	a[2].Status = StatusPaused
	m.workers[a[0].ID] = workerHandle{token: "w1", cancel: func() { cancelled1 = true }}
	m.workers[a[1].ID] = workerHandle{token: "w2", cancel: func() { cancelled2 = true }}
	origOrders := []uint64{a[0].QueueOrder, a[1].QueueOrder, a[2].QueueOrder}
	m.mu.Unlock()

	if err := os.Remove(m.cfgMgr.GetTasksPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(m.cfgMgr.GetTasksPath(), 0700); err != nil {
		t.Fatal(err)
	}

	if err := m.PrioritizeTasks([]string{a[2].ID}); err == nil {
		t.Fatal("expected persistence failure to return an error")
	}

	if got := queueTitles(m); !reflect.DeepEqual(got, []string{"A1", "A2", "A3"}) {
		t.Fatalf("queue order changed despite persistence failure: %v", got)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if a[0].QueueOrder != origOrders[0] || a[1].QueueOrder != origOrders[1] || a[2].QueueOrder != origOrders[2] {
		t.Fatalf("queueOrder mutated after rollback")
	}
	if a[0].Status != StatusDownloading || a[1].Status != StatusDownloading || a[2].Status != StatusPaused {
		t.Fatalf("statuses mutated after rollback: %s %s %s", a[0].Status, a[1].Status, a[2].Status)
	}
	if cancelled1 || cancelled2 || m.workers[a[0].ID].invalidated || m.workers[a[1].ID].invalidated {
		t.Fatal("workers were cancelled/invalidated despite persistence failure")
	}
}

func BenchmarkQueueBatch1000(b *testing.B) {
	for i := 0; i < b.N; i++ {
		m := NewDownloadManager(config.NewConfigManager(b.TempDir()))
		req := &DownloadRequest{Title: "large batch", IsBangumi: true}
		episodes := make([]bilibili.EpisodeInfo, 1000)
		for n := range episodes {
			episodes[n] = bilibili.EpisodeInfo{CID: int64(n + 1), Index: n + 1, Title: fmt.Sprintf("part %d", n+1)}
		}
		if _, err := m.AddDownloadTasks(req, episodes); err != nil {
			b.Fatal(err)
		}
	}
}
