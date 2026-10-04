package downloader

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

func writeStorageFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestRepeatedDownloadReusesTaskAndCache(t *testing.T) {
	for _, status := range []TaskStatus{StatusQueued, StatusPaused, StatusError, StatusCancelled, StatusCompleted} {
		t.Run(string(status), func(t *testing.T) {
			m := newTestManager(t)
			req := &DownloadRequest{Title: "same video", TargetQuality: "80", TargetCodec: "AVC"}
			ep := &bilibili.EpisodeInfo{CID: 10, Title: req.Title}
			first, err := m.AddDownloadTask(req, ep)
			if err != nil {
				t.Fatal(err)
			}
			if !pathWithin(m.cfgMgr.GetDownloadCacheDir(), first.VideoTmpPath) || pathWithin(m.cfgMgr.Get().DownloadDir, first.VideoTmpPath) {
				t.Fatalf("temporary file must be inside cache, got %s", first.VideoTmpPath)
			}
			first.Status = status
			if status == StatusCompleted {
				writeStorageFixture(t, first.OutputPath, []byte("finished media"))
			}
			second, err := m.AddDownloadTask(req, ep)
			if err != nil || second.ID != first.ID || len(m.GetTasks()) != 1 {
				t.Fatalf("duplicate task created: %v", err)
			}
			want := StatusQueued
			if status == StatusCompleted {
				want = StatusCompleted
			}
			if second.Status != want {
				t.Fatalf("got %s, want %s", second.Status, want)
			}
			if status == StatusCompleted {
				if err := os.Remove(second.OutputPath); err != nil {
					t.Fatal(err)
				}
				third, err := m.AddDownloadTask(req, ep)
				if err != nil || third.ID != first.ID || third.Status != StatusQueued || len(m.GetTasks()) != 1 {
					t.Fatalf("missing file must requeue original task: %v", err)
				}
			}
		})
	}
}

func TestOutputPathReservationPreservesExistingFilesAndTasks(t *testing.T) {
	m := newTestManager(t)
	path := filepath.Join(m.cfgMgr.Get().DownloadDir, "video [1080P].mp4")
	writeStorageFixture(t, path, []byte("keep original"))
	m.tasks = []*DownloadTask{{ID: "other", OutputPath: filepath.Join(filepath.Dir(path), "video [1080P] (1).mp4")}}
	got, err := m.availableOutputPathLocked(path, "new")
	if err != nil || !strings.HasSuffix(got, " (2).mp4") {
		t.Fatalf("file/task collision not avoided: %s %v", got, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "keep original" {
		t.Fatal("existing file changed")
	}
}

func TestStorageRecoveryMigratesPartsDeduplicatesHistoryAndCleansOrphans(t *testing.T) {
	m := newTestManager(t)
	dir := m.cfgMgr.Get().DownloadDir
	old := filepath.Join(dir, "video.12345678.video.downloading")
	part := streamSegmentPath(old, 7)
	writeStorageFixture(t, old, []byte("prefix"))
	writeStorageFixture(t, part, []byte("remaining"))
	orphan := filepath.Join(dir, "video.81bc2857.video.downloading.part-00")
	writeStorageFixture(t, orphan, []byte("orphan"))
	unrelated := filepath.Join(dir, "personal.part-00")
	writeStorageFixture(t, unrelated, []byte("keep"))
	output := filepath.Join(dir, "finished.mp4")
	writeStorageFixture(t, output, []byte("finished"))
	m.tasks = []*DownloadTask{
		{ID: "paused", Status: StatusPaused, VideoTmpPath: old},
		{ID: "older", Status: StatusCompleted, OutputPath: output, CompletedAt: 1},
		{ID: "newer", Status: StatusCompleted, OutputPath: output, CompletedAt: 2},
	}
	if err := m.SaveTasks(); err != nil {
		t.Fatal(err)
	}
	loaded := NewDownloadManager(m.cfgMgr)
	tasks := loaded.GetTasks()
	if len(tasks) != 2 || tasks[1].ID != "newer" {
		t.Fatalf("history was not deduplicated: %+v", tasks)
	}
	for path, want := range map[string]string{tasks[0].VideoTmpPath: "prefix", streamSegmentPath(tasks[0].VideoTmpPath, 7): "remaining", unrelated: "keep", output: "finished"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("lost data at %s: %q %v", path, got, err)
		}
	}
	for _, path := range []string{old, part, orphan} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("old artifact remains: %s", path)
		}
	}
	data, _ := os.ReadFile(m.cfgMgr.GetTasksPath())
	var persisted []*DownloadTask
	if err := json.Unmarshal(data, &persisted); err != nil || len(persisted) != 2 || persisted[0].VideoTmpPath != tasks[0].VideoTmpPath {
		t.Fatalf("recovery not persisted: %v", err)
	}
}

func TestStorageMigrationFailurePreservesResumableParts(t *testing.T) {
	m := newTestManager(t)
	old := filepath.Join(m.cfgMgr.Get().DownloadDir, "video.12345678.video.downloading")
	part := streamSegmentPath(old, 0)
	writeStorageFixture(t, part, []byte("resume me"))
	writeStorageFixture(t, m.cfgMgr.GetDownloadCacheDir(), []byte("blocks cache directory"))
	m.tasks = []*DownloadTask{{ID: "paused", Status: StatusPaused, VideoTmpPath: old}}
	m.reconcileTaskStorage()
	data, err := os.ReadFile(part)
	if err != nil || string(data) != "resume me" || m.tasks[0].VideoTmpPath != old {
		t.Fatalf("failed migration lost resumable data: %v", err)
	}
}

func TestDeleteTaskWaitsForWorkerThenRemovesAllParts(t *testing.T) {
	m := newTestManager(t)
	video, audio := m.cachePaths(newUUID())
	done := make(chan struct{})
	task := &DownloadTask{ID: "running", Status: StatusDownloading, VideoTmpPath: video, AudioTmpPath: audio}
	m.tasks = []*DownloadTask{task}
	m.workers[task.ID] = workerHandle{done: done, cancel: func() {}}
	part := streamSegmentPath(video, 7)
	writeStorageFixture(t, part, []byte("still writing"))
	if err := m.DeleteTask(task.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(part); err != nil {
		t.Fatal("must not clean while worker is still running")
	}
	close(done)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Dir(video)); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(part)
	if bytes.Equal(data, []byte("still writing")) {
		t.Fatal("part not removed after worker exited")
	}
	t.Fatal("empty cache directory not removed")
}

func TestStoragePersistenceFailureKeepsOriginalFilesAndPaths(t *testing.T) {
	m := newTestManager(t)
	old := filepath.Join(m.cfgMgr.Get().DownloadDir, "video.12345678.video.downloading")
	writeStorageFixture(t, streamSegmentPath(old, 0), []byte("original"))
	m.tasks = []*DownloadTask{{ID: newUUID(), Status: StatusPaused, VideoTmpPath: old}}
	// A directory at tasks.json forces persistence to fail after copying files.
	if err := os.Mkdir(m.cfgMgr.GetTasksPath(), 0755); err != nil {
		t.Fatal(err)
	}
	m.reconcileTaskStorage()
	data, err := os.ReadFile(streamSegmentPath(old, 0))
	if err != nil || string(data) != "original" || m.tasks[0].VideoTmpPath != old {
		t.Fatalf("failed persistence lost original data or changed paths: %v", err)
	}
}

func TestCacheCleanupPreservesPendingTasksAndUnrelatedFiles(t *testing.T) {
	m := newTestManager(t)
	video, audio := m.cachePaths(newUUID())
	orphanVideo, _ := m.cachePaths(newUUID())
	writeStorageFixture(t, streamSegmentPath(video, 0), []byte("pending"))
	writeStorageFixture(t, streamSegmentPath(orphanVideo, 7), []byte("orphan"))
	unrelated := filepath.Join(m.cfgMgr.GetDownloadCacheDir(), "personal", "notes.txt")
	writeStorageFixture(t, unrelated, []byte("notes"))
	m.tasks = []*DownloadTask{{ID: "pending", Status: StatusPaused, VideoTmpPath: video, AudioTmpPath: audio}}
	m.cleanupCacheOrphans()
	for _, path := range []string{streamSegmentPath(video, 0), unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("protected file deleted: %s", path)
		}
	}
	if _, err := os.Stat(filepath.Dir(orphanVideo)); !os.IsNotExist(err) {
		t.Fatal("orphan cache not cleaned")
	}
}
