package downloader

import (
	"path/filepath"
	"strings"
	"testing"

	"bilibili_downloader/pkg/bilibili"
)

func TestCourseTaskKeepsSourceAndCourseFolderAcrossRestart(t *testing.T) {
	manager := newTestManager(t)
	episode := &bilibili.EpisodeInfo{Index: 2, CID: 302, EPID: 102, AID: 202, Title: "正式课", Duration: 772}
	request := &DownloadRequest{IsCheese: true, SSID: 44, Title: "课程", TargetQuality: "highest", TargetCodec: "auto", Episodes: []int64{302}}
	task, err := manager.AddDownloadTask(request, episode)
	if err != nil {
		t.Fatal(err)
	}
	if !task.IsCheese || task.IsBangumi || task.EPID != 102 || !strings.HasSuffix(filepath.Dir(task.OutputPath), "课程 [ss44]") {
		t.Fatalf("course task lost its source, episode, or collection directory: %+v", task)
	}
	// A normal video with the same CID must not reuse a course player request.
	normalRequest := *request
	normalRequest.IsCheese = false
	normal, err := manager.AddDownloadTask(&normalRequest, episode)
	if err != nil || normal.ID == task.ID {
		t.Fatal("normal and course tasks incorrectly share a download identity")
	}
	reloaded := NewDownloadManager(manager.cfgMgr)
	var found bool
	for _, loaded := range reloaded.GetTasks() {
		if loaded.ID == task.ID {
			found = loaded.IsCheese && !loaded.IsBangumi && loaded.EPID == 102 && loaded.OutputPath == task.OutputPath
		}
	}
	if !found {
		t.Fatal("restarting the app lost the course download source")
	}
}
