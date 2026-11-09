package downloader

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
	"bilibili_downloader/pkg/utils"
)

func TestNestedCollectionDirectoryAndHistory(t *testing.T) {
	m := newTestManager(t)
	req := &DownloadRequest{Title: "钢琴教程", CollectionID: 123, TargetQuality: "80", TargetCodec: "AVC", Episodes: []int64{11, 12, 21}}
	eps := []*bilibili.EpisodeInfo{
		{Index: 1, Page: 1, CID: 11, BVID: "BVone", Title: "入门", VideoTitle: "第一册", PartCount: 2},
		{Index: 1, Page: 2, CID: 12, BVID: "BVone", Title: "练习", VideoTitle: "第一册", PartCount: 2},
		{Index: 2, Page: 1, CID: 21, BVID: "BVtwo", Title: "单集", VideoTitle: "第二册", PartCount: 1},
	}
	var tasks []*DownloadTask
	for _, ep := range eps {
		task, err := m.AddDownloadTask(req, ep)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	root := filepath.Join(m.cfgMgr.Get().DownloadDir, "钢琴教程")
	if filepath.Dir(tasks[0].OutputPath) != filepath.Join(root, "01. 第一册") || filepath.Dir(tasks[1].OutputPath) != filepath.Join(root, "01. 第一册") || filepath.Dir(tasks[2].OutputPath) != root {
		t.Fatalf("incorrect hierarchy: %+v", tasks)
	}
	if tasks[1].CollectionTitle != "钢琴教程" || tasks[1].VideoTitle != "第一册" || tasks[1].PartTitle != "练习" {
		t.Fatalf("history missing hierarchy: %+v", tasks[1])
	}
	m.tasks = nil
	other, err := m.AddDownloadTask(&DownloadRequest{Title: "钢琴教程", CollectionID: 456}, &bilibili.EpisodeInfo{CID: 31, BVID: "BVthree", Index: 1, Title: "视频"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(other.OutputPath) == root {
		t.Fatal("different collection must not reuse same directory")
	}
	same, err := m.AddDownloadTask(req, &bilibili.EpisodeInfo{CID: 13, BVID: "BVone", Index: 1, Page: 3, PartCount: 3, VideoTitle: "第一册", Title: "第三P"})
	if err != nil || filepath.Dir(same.OutputPath) != filepath.Join(root, "01. 第一册") {
		t.Fatalf("directory identity lost after clearing history: %v %+v", err, same)
	}
}

func TestCollectionDirectoryIdentitySurvivesEmptyCleanupAndRestart(t *testing.T) {
	m := newTestManager(t)
	add := func(collection int64) *DownloadTask {
		t.Helper()
		task, err := m.AddDownloadTask(&DownloadRequest{Title: "同名合集", CollectionID: collection},
			&bilibili.EpisodeInfo{BVID: "BVentry", CID: collection, Index: 1, Title: "分集"})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	first := add(1)
	if err := m.DeleteTask(first.ID, false); err != nil {
		t.Fatal(err)
	}
	waitCollectionCleanup(t, func() bool { return pathIsGone(first.CollectionDir) })
	m = NewDownloadManager(m.cfgMgr)
	second := add(2)
	again := add(1)
	if again.CollectionDir != first.CollectionDir || again.CollectionDir == second.CollectionDir {
		t.Fatalf("directories lost stable ownership: %q, %q, %q", first.CollectionDir, second.CollectionDir, again.CollectionDir)
	}
}

func TestCollectionDirectoryRepairsExistingDuplicateMapping(t *testing.T) {
	m := newTestManager(t)
	root := m.cfgMgr.Get().DownloadDir
	path := filepath.Join(root, "same")
	file := filepath.Join(path, "existing.mp4")
	writeStorageFixture(t, file, []byte("existing"))
	index := filepath.Join(filepath.Dir(m.cfgMgr.GetTasksPath()), "collection-directories.json")
	data, err := json.Marshal(map[string]string{pathKey(root) + "|ugc:1": path, pathKey(root) + "|ugc:2": path})
	if err != nil {
		t.Fatal(err)
	}
	writeStorageFixture(t, index, data)
	first, err := m.collectionDir(root, "ugc:1", "same", 2, func() []string { return []string{path} })
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.collectionDir(root, "ugc:2", "same", 2, nil)
	if err != nil || first == second || second != path {
		t.Fatalf("duplicate ownership was not repaired: %q, %q, %v", first, second, err)
	}
	assertMediaBytes(t, file, []byte("existing"))
}

func TestCollectionRegistryRecoversMalformedAndNullData(t *testing.T) {
	for _, content := range []string{"null", "{broken"} {
		t.Run(content, func(t *testing.T) {
			m := newTestManager(t)
			index := filepath.Join(filepath.Dir(m.cfgMgr.GetTasksPath()), "collection-directories.json")
			if err := os.WriteFile(index, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			task, err := m.AddDownloadTask(&DownloadRequest{CollectionID: 123, Title: "教程"}, &bilibili.EpisodeInfo{BVID: "BVtest", CID: 11, Index: 1})
			if err != nil || task == nil {
				t.Fatalf("download blocked by bad index: %v", err)
			}
			backups, err := filepath.Glob(index + ".invalid-*.json")
			if err != nil || len(backups) != 1 {
				t.Fatalf("original was not preserved: %v %v", backups, err)
			}
			data, _ := os.ReadFile(backups[0])
			if !bytes.Equal(data, []byte(content)) {
				t.Fatal("backup content changed")
			}
		})
	}
}

func TestCollectionRegistryUnavailableStillCreatesTasksAndRecoversFromHistory(t *testing.T) {
	m := newTestManager(t)
	index := filepath.Join(filepath.Dir(m.cfgMgr.GetTasksPath()), "collection-directories.json")
	if err := os.Mkdir(index, 0700); err != nil {
		t.Fatal(err)
	}
	req := &DownloadRequest{CollectionID: 123, Title: "教程"}
	ep := &bilibili.EpisodeInfo{BVID: "BVtest", CID: 11, Index: 1, Page: 1, PartCount: 2, VideoTitle: "第一册"}
	first, err := m.AddDownloadTask(req, ep)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh manager has no in-memory association, and the index is still unreadable.
	restarted := NewDownloadManager(m.cfgMgr)
	next := *ep
	next.CID, next.Page = 12, 2
	second, err := restarted.AddDownloadTask(req, &next)
	if err != nil || filepath.Dir(second.OutputPath) != filepath.Dir(first.OutputPath) {
		t.Fatalf("history did not recover directory: %v", err)
	}
}

func TestCollectionReusesProvenLegacyDirectory(t *testing.T) {
	m := newTestManager(t)
	dir := filepath.Join(m.cfgMgr.Get().DownloadDir, "旧教程 [BVtest]")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "P01.mp4")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	m.tasks = []*DownloadTask{{ID: "old", BVID: "BVtest", CID: 11, Title: "教程", OutputPath: file, Status: StatusCompleted}}
	task, err := m.AddDownloadTask(&DownloadRequest{BVID: "BVtest", Title: "教程", IsCollection: true}, &bilibili.EpisodeInfo{BVID: "BVtest", CID: 12, Index: 2, Title: "练习"})
	if err != nil || filepath.Dir(task.OutputPath) != dir {
		t.Fatalf("legacy directory not reused: %v", err)
	}
	data, _ := os.ReadFile(file)
	if string(data) != "original" {
		t.Fatal("legacy media changed")
	}
}

func TestNestedCollectionBudgetsDeepPathAndEmoji(t *testing.T) {
	m := newTestManager(t)
	cfg := m.cfgMgr.Get()
	// Build a root around 120 UTF-16 units, where the old per-layer budget
	// allowed the first folder to consume the child's remaining space.
	cfg.DownloadDir = filepath.Join(t.TempDir(), "destination")
	if n := 120 - utils.WindowsPathUnits(cfg.DownloadDir); n > 0 {
		cfg.DownloadDir += strings.Repeat("x", n)
	}
	if err := m.cfgMgr.Save(cfg); err != nil {
		t.Fatal(err)
	}
	task, err := m.AddDownloadTask(&DownloadRequest{CollectionID: 123, Title: strings.Repeat("😀教程", 30)}, &bilibili.EpisodeInfo{BVID: "BVtest", CID: 11, Index: 1, Page: 1, PartCount: 100, VideoTitle: strings.Repeat("😀第一册", 30), Title: strings.Repeat("😀练习", 30)})
	if err != nil {
		t.Fatal(err)
	}
	if units := utils.WindowsPathUnits(task.OutputPath); units > 210 {
		t.Fatalf("path has %d Windows units", units)
	}
}

func TestCollectionCustomFilenameTemplate(t *testing.T) {
	m := newTestManager(t)
	cfg := m.cfgMgr.Get()
	cfg.FileNameTemplate = "{bvid}_{index}_{part}"
	if err := m.cfgMgr.Save(cfg); err != nil {
		t.Fatal(err)
	}
	task, err := m.AddDownloadTask(&DownloadRequest{CollectionID: 123, Title: "教程"}, &bilibili.EpisodeInfo{BVID: "BVtest", CID: 11, Index: 1, Page: 2, PartCount: 3, VideoTitle: "第一册", Title: "练习"})
	if err != nil || filepath.Base(task.OutputPath) != "BVtest_P02_练习.mp4" {
		t.Fatalf("custom template ignored: %+v %v", task, err)
	}
}

func TestCollectionDirectoryWaitDoesNotBlockTaskSnapshots(t *testing.T) {
	m := newTestManager(t)
	m.directoryMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := m.AddDownloadTask(&DownloadRequest{CollectionID: 123, Title: "教程"}, &bilibili.EpisodeInfo{BVID: "BVtest", CID: 11})
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for m.addMu.TryLock() {
		m.addMu.Unlock()
		if time.Now().After(deadline) {
			m.directoryMu.Unlock()
			t.Fatal("task creation did not start")
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
		t.Fatal("directory wait held the task mutex")
	}
	m.directoryMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentCollectionAddsKeepOneTaskAndDirectory(t *testing.T) {
	m := newTestManager(t)
	req := &DownloadRequest{CollectionID: 123, Title: "教程"}
	ep := &bilibili.EpisodeInfo{BVID: "BVtest", CID: 11, Index: 1, Page: 1, PartCount: 2, VideoTitle: "第一册"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.AddDownloadTask(req, ep); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if tasks := m.GetTasks(); len(tasks) != 1 {
		t.Fatalf("duplicate tasks: %d", len(tasks))
	}
}

func TestCollectionDirectoryDoesNotAdoptUnknownFolder(t *testing.T) {
	m := newTestManager(t)
	parent := m.cfgMgr.Get().DownloadDir
	existing := filepath.Join(parent, "同名合集")
	if err := os.MkdirAll(existing, 0755); err != nil {
		t.Fatal(err)
	}
	got, err := m.collectionDir(parent, "ugc:5", "同名合集", 2, nil)
	if err != nil || got == existing {
		t.Fatalf("unknown folder adopted: %q %v", got, err)
	}
}
