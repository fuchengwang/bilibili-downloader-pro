package downloader

import (
	"context"
	"testing"
	"time"
)

func TestRestartRefusesMergeWithoutCancellingIt(t *testing.T) {
	mgr := newTestManager(t)
	cancelled := false
	mgr.mu.Lock()
	mgr.tasks = append(mgr.tasks, &DownloadTask{ID: "merge", Status: StatusMerging})
	mgr.workers["merge"] = workerHandle{token: "merge", cancel: func() { cancelled = true }}
	mgr.mu.Unlock()
	if err := mgr.PrepareForRestart(context.Background()); err == nil {
		t.Fatal("restart allowed during merge")
	}
	if cancelled {
		t.Fatal("refusing a restart cancelled the merge")
	}
	if mgr.GetTasks()[0].Status != StatusMerging {
		t.Fatal("merge status was changed")
	}
}
func TestRestartWaitsForClosedDownloadFilesAndPersistsPausedTasks(t *testing.T) {
	mgr := newTestManager(t)
	workerCtx, cancel := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	mayClose := make(chan struct{})
	mgr.mu.Lock()
	mgr.tasks = append(mgr.tasks, &DownloadTask{ID: "download", Status: StatusDownloading})
	mgr.workers["download"] = workerHandle{token: "download", cancel: cancel, done: workerDone}
	mgr.mu.Unlock()
	go func() { <-workerCtx.Done(); <-mayClose; close(workerDone) }()
	result := make(chan error, 1)
	go func() { result <- mgr.PrepareForRestart(context.Background()) }()
	<-workerCtx.Done()
	select {
	case err := <-result:
		t.Fatal("restart didn't wait for worker to release files", err)
	default:
	}
	if mgr.setWorkerTaskStatus(mgr.tasks[0], "download", StatusDownloading, StatusMerging, "") {
		t.Fatal("invalidated worker entered merge")
	}
	close(mayClose)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("restart wait hung")
	}
	restored := NewDownloadManager(mgr.cfgMgr)
	if tasks := restored.GetTasks(); len(tasks) != 1 || tasks[0].Status != StatusPaused {
		t.Fatal("paused tasks not saved", tasks)
	}
}
