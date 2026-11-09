package downloader

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

func cleanupCollectionFixture(t *testing.T, nested bool) (*DownloadManager, []*DownloadTask, string) {
	t.Helper()
	m := newTestManager(t)
	req := &DownloadRequest{Title: "清理测试合集", CollectionID: 123, Episodes: []int64{11, 12}}
	var tasks []*DownloadTask
	for i := 1; i <= 2; i++ {
		ep := &bilibili.EpisodeInfo{BVID: "BVcleanup", CID: int64(10 + i), Index: i, Page: 1, PartCount: 1, Title: "视频", VideoTitle: "单集"}
		if nested {
			ep.Index = 1
			ep.Page = i
			ep.PartCount = 2
			ep.VideoTitle = "多P视频"
		}
		task, err := m.AddDownloadTask(req, ep)
		if err != nil {
			t.Fatal(err)
		}
		if err := m.relocateTaskStreams(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		task.Status = StatusCompleted
		writeStorageFixture(t, task.OutputPath, []byte("finished media"))
		writeStorageFixture(t, task.VideoTmpPath, []byte("retained video"))
		writeStorageFixture(t, task.AudioTmpPath, []byte("retained audio"))
		if err := ensureRangeMarker(task.VideoTmpPath); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	return m, tasks, tasks[0].CollectionDir
}

func waitCollectionCleanup(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("asynchronous collection cleanup did not finish")
}

func pathIsGone(path string) bool { _, err := os.Lstat(path); return os.IsNotExist(err) }

func TestCollectionCleanupAfterLastEpisodeAndNestedParents(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(map[bool]string{false: "collection", true: "nested collection"}[nested], func(t *testing.T) {
			m, tasks, top := cleanupCollectionFixture(t, nested)
			if err := m.DeleteTask(tasks[0].ID, true); err != nil {
				t.Fatal(err)
			}
			waitCollectionCleanup(t, func() bool {
				return pathIsGone(tasks[0].OutputPath) && pathIsGone(rangeMarkerPath(tasks[0].VideoTmpPath))
			})
			if _, err := os.Stat(tasks[1].OutputPath); err != nil {
				t.Fatal("deleting one episode damaged another")
			}
			if _, err := os.Stat(top); err != nil {
				t.Fatal("collection disappeared before its last episode")
			}
			if err := m.DeleteTask(tasks[1].ID, true); err != nil {
				t.Fatal(err)
			}
			waitCollectionCleanup(t, func() bool { return pathIsGone(top) })
			if _, err := os.Stat(m.cfgMgr.Get().DownloadDir); err != nil {
				t.Fatal("download root was removed")
			}
		})
	}
}

func TestCollectionCleanupPreservesAllUserEntries(t *testing.T) {
	for _, name := range []string{"notes.txt", ".DS_Store", "Thumbs.db", "empty-user-folder"} {
		t.Run(name, func(t *testing.T) {
			m, _, top := cleanupCollectionFixture(t, true)
			userPath := filepath.Join(top, name)
			if name == "empty-user-folder" {
				if err := os.Mkdir(userPath, 0755); err != nil {
					t.Fatal(err)
				}
			} else {
				writeStorageFixture(t, userPath, []byte("keep user data"))
			}
			if err := m.ClearCompleted(true); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(userPath); err != nil {
				t.Fatalf("user entry removed: %v", err)
			}
			if name != "empty-user-folder" {
				got, _ := os.ReadFile(userPath)
				if string(got) != "keep user data" {
					t.Fatal("user file changed")
				}
			}
		})
	}
}

func TestCollectionCleanupClearAndRegistryReuseAfterRestart(t *testing.T) {
	m, tasks, top := cleanupCollectionFixture(t, true)
	if err := m.SaveTasks(); err != nil {
		t.Fatal(err)
	}
	m = NewDownloadManager(m.cfgMgr)
	if got := m.GetTasks()[0]; got.CollectionDir != top || got.DownloadRoot != m.cfgMgr.Get().DownloadDir {
		t.Fatal("restart lost cleanup boundaries")
	}
	if err := m.ClearCompleted(true); err != nil {
		t.Fatal(err)
	}
	if !pathIsGone(top) {
		t.Fatal("batch deletion left an empty collection")
	}
	recreated, err := m.AddDownloadTask(&DownloadRequest{Title: "清理测试合集", CollectionID: 123}, &bilibili.EpisodeInfo{CID: 13, BVID: "BVcleanup", Index: 1, Page: 3, PartCount: 3, VideoTitle: "多P视频", Title: "新一集"})
	if err != nil || filepath.Dir(recreated.OutputPath) != filepath.Dir(tasks[0].OutputPath) {
		t.Fatalf("deleted collection did not recreate correctly: %v", err)
	}
}

func TestCollectionCleanupProtectsQueuedTaskWithoutFiles(t *testing.T) {
	m, tasks, top := cleanupCollectionFixture(t, true)
	tasks[1].Status = StatusPaused
	cleanupStreamArtifacts(tasks[1].VideoTmpPath)
	cleanupStreamArtifacts(tasks[1].AudioTmpPath)
	if err := os.Remove(tasks[1].OutputPath); err != nil {
		t.Fatal(err)
	}
	if err := m.ClearCompleted(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(tasks[1].OutputPath)); err != nil {
		t.Fatal("pending task directory was removed")
	}
	if err := m.DeleteTask(tasks[1].ID, false); err != nil {
		t.Fatal(err)
	}
	waitCollectionCleanup(t, func() bool { return pathIsGone(top) })
}

func TestCollectionCleanupWaitsForDeletedWorker(t *testing.T) {
	m, tasks, top := cleanupCollectionFixture(t, false)
	if err := m.ClearCompleted(true); err != nil {
		t.Fatal(err)
	}
	// Recreate one task and model its still-running file writer.
	tasks[0].Status = StatusDownloading
	writeStorageFixture(t, tasks[0].VideoTmpPath, []byte("still writing"))
	m.tasks = []*DownloadTask{tasks[0]}
	done := make(chan struct{})
	m.workers[tasks[0].ID] = workerHandle{done: done, cancel: func() {}}
	if err := m.DeleteTask(tasks[0].ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tasks[0].VideoTmpPath); err != nil {
		t.Fatal("cleanup ran before worker exit")
	}
	close(done)
	waitCollectionCleanup(t, func() bool { return pathIsGone(top) })
}

func TestCollectionCleanupPreservesAnotherDownloadInSameFolder(t *testing.T) {
	m, tasks, top := cleanupCollectionFixture(t, true)
	for _, task := range tasks {
		task.Status = StatusDownloading
		if err := os.Remove(task.OutputPath); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.DeleteTask(tasks[0].ID, false); err != nil {
		t.Fatal(err)
	}
	waitCollectionCleanup(t, func() bool { return pathIsGone(rangeMarkerPath(tasks[0].VideoTmpPath)) })
	got, err := os.ReadFile(tasks[1].VideoTmpPath)
	if err != nil || string(got) != "retained video" {
		t.Fatalf("remaining download was damaged: %q, %v", got, err)
	}
	if err := m.DeleteTask(tasks[1].ID, false); err != nil {
		t.Fatal(err)
	}
	waitCollectionCleanup(t, func() bool { return pathIsGone(top) })
}

func TestCollectionCleanupKeepsFoldersWhenOnlyRemovingRecords(t *testing.T) {
	m, tasks, top := cleanupCollectionFixture(t, false)
	for _, task := range tasks {
		if err := os.Remove(task.OutputPath); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.ClearCompleted(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(top); err != nil {
		t.Fatal("record-only deletion removed a directory")
	}
}

func TestCollectionCleanupFailureToPersistKeepsFilesAndFolders(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "batch"}[batch], func(t *testing.T) {
			m, tasks, top := cleanupCollectionFixture(t, false)
			if err := os.Remove(m.cfgMgr.GetTasksPath()); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(m.cfgMgr.GetTasksPath(), 0755); err != nil {
				t.Fatal(err)
			}
			var err error
			if batch {
				err = m.ClearCompleted(true)
			} else {
				err = m.DeleteTask(tasks[0].ID, true)
			}
			if err == nil {
				t.Fatal("expected save failure")
			}
			if _, err := os.Stat(top); err != nil {
				t.Fatal("save failure removed folder")
			}
			for _, task := range tasks {
				if _, err := os.Stat(task.OutputPath); err != nil {
					t.Fatal("save failure deleted media")
				}
			}
		})
	}
}

func TestCollectionCleanupBoundariesAndChangedDownloadRoot(t *testing.T) {
	m, tasks, top := cleanupCollectionFixture(t, true)
	oldRoot := tasks[0].DownloadRoot
	settings := m.cfgMgr.Get()
	settings.DownloadDir = filepath.Join(t.TempDir(), "new-downloads")
	if err := os.MkdirAll(settings.DownloadDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := m.cfgMgr.Save(settings); err != nil {
		t.Fatal(err)
	}
	if err := m.ClearCompleted(true); err != nil {
		t.Fatal(err)
	}
	if !pathIsGone(top) {
		t.Fatal("changing download root left the old empty collection")
	}
	for _, root := range []string{oldRoot, settings.DownloadDir} {
		if _, err := os.Stat(root); err != nil {
			t.Fatal("a download root was removed")
		}
	}
	// Even if a collection itself becomes the selected root, retain it.
	m, tasks, top = cleanupCollectionFixture(t, false)
	settings = m.cfgMgr.Get()
	settings.DownloadDir = top
	if err := m.cfgMgr.Save(settings); err != nil {
		t.Fatal(err)
	}
	if err := m.ClearCompleted(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(top); err != nil {
		t.Fatal("current selected root was removed")
	}
}

func TestCollectionCleanupRejectsUnprovenAndOutsidePaths(t *testing.T) {
	m := newTestManager(t)
	outside := filepath.Join(t.TempDir(), "user-folder")
	if err := os.Mkdir(outside, 0755); err != nil {
		t.Fatal(err)
	}
	task := &DownloadTask{OutputPath: filepath.Join(outside, "video.mp4"), DownloadRoot: m.cfgMgr.Get().DownloadDir, CollectionDir: outside}
	m.cleanupEmptyCollectionDirs([]*DownloadTask{task})
	if pathIsGone(outside) {
		t.Fatal("outside folder was removed")
	}
	unknown := filepath.Join(m.cfgMgr.Get().DownloadDir, "unproven")
	if err := os.MkdirAll(unknown, 0755); err != nil {
		t.Fatal(err)
	}
	m.cleanupEmptyCollectionDirs([]*DownloadTask{{OutputPath: filepath.Join(unknown, "video.mp4"), BVID: "BVunknown"}})
	if pathIsGone(unknown) {
		t.Fatal("unproven legacy folder was removed")
	}
}

func TestCollectionCleanupLegacyRegistryProvesOwnership(t *testing.T) {
	m, tasks, top := cleanupCollectionFixture(t, true)
	for _, task := range tasks {
		task.DownloadRoot = ""
		task.CollectionDir = ""
	}
	if err := m.SaveTasks(); err != nil {
		t.Fatal(err)
	}
	m = NewDownloadManager(m.cfgMgr)
	if err := m.ClearCompleted(true); err != nil {
		t.Fatal(err)
	}
	if !pathIsGone(top) {
		t.Fatal("registered legacy collection was not cleaned")
	}
}

func TestCollectionCleanupDoesNotFollowDirectoryLinks(t *testing.T) {
	m, tasks, top := cleanupCollectionFixture(t, true)
	if err := m.ClearCompleted(true); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, top); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	m.cleanupEmptyCollectionDirs(tasks)
	if _, err := os.Lstat(top); err != nil {
		t.Fatal("directory link was removed")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("link target was removed")
	}
}

func TestCollectionCleanupProtectsEveryRecordedDownloadRoot(t *testing.T) {
	m, tasks, top := cleanupCollectionFixture(t, false)
	for _, task := range tasks {
		cleanupStreamArtifacts(task.VideoTmpPath)
		cleanupStreamArtifacts(task.AudioTmpPath)
		if err := os.Remove(task.OutputPath); err != nil {
			t.Fatal(err)
		}
	}
	child := filepath.Join(top, "another-collection")
	if err := os.Mkdir(child, 0755); err != nil {
		t.Fatal(err)
	}
	// This folder was a collection for one task, then a download root for
	// another. Batch deletion must preserve all recorded root boundaries.
	other := &DownloadTask{OutputPath: filepath.Join(child, "episode.mp4"), DownloadRoot: top, CollectionDir: child}
	m.tasks = nil
	m.cleanupEmptyCollectionDirs(append(tasks, other))
	if !pathIsGone(child) {
		t.Fatal("nested collection was not removed")
	}
	if _, err := os.Stat(top); err != nil {
		t.Fatal("another task's original download root was removed")
	}
}

func TestCollectionCleanupSkipsSymlinkAncestorAndFileReplacement(t *testing.T) {
	m, tasks, top := cleanupCollectionFixture(t, true)
	child := filepath.Dir(tasks[0].OutputPath)
	if err := m.ClearCompleted(true); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(top, 0755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, child); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	m.cleanupEmptyCollectionDirs(tasks)
	if _, err := os.Lstat(child); err != nil {
		t.Fatal("ancestor link was removed")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("ancestor link target was removed")
	}
	if err := os.Remove(child); err != nil {
		t.Fatal(err)
	}
	writeStorageFixture(t, child, []byte("user replaced directory with a file"))
	m.cleanupEmptyCollectionDirs(tasks)
	got, err := os.ReadFile(child)
	if err != nil || string(got) != "user replaced directory with a file" {
		t.Fatal("cleanup deleted or changed a regular file")
	}
}

func TestCollectionCleanupDoesNotHoldProgressLockWhileWaitingForDirectoryIndex(t *testing.T) {
	m, tasks, _ := cleanupCollectionFixture(t, false)
	for _, task := range tasks {
		task.DownloadRoot = ""
		task.CollectionDir = ""
	}
	m.directoryMu.Lock()
	done := make(chan struct{})
	go func() { m.cleanupEmptyCollectionDirs(tasks); close(done) }()
	deadline := time.Now().Add(time.Second)
	for m.addMu.TryLock() {
		m.addMu.Unlock()
		if time.Now().After(deadline) {
			m.directoryMu.Unlock()
			<-done
			t.Fatal("cleanup did not start")
		}
		time.Sleep(time.Millisecond)
	}
	snapshot := make(chan struct{})
	go func() { m.GetTasks(); close(snapshot) }()
	select {
	case <-snapshot:
	case <-time.After(time.Second):
		m.directoryMu.Unlock()
		<-done
		t.Fatal("directory I/O blocked task snapshots")
	}
	m.directoryMu.Unlock()
	<-done
}
