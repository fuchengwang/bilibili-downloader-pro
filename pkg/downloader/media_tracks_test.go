package downloader

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMediaTracksDiscoverBothLengthsWithoutTinyProbes(t *testing.T) {
	video, audio := engineData(256*1024), engineData(32*1024)
	var headers atomic.Int32
	both := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			t.Error("parser hints should avoid mandatory tiny probes")
			w.WriteHeader(503)
			return
		}
		if headers.Add(1) == 2 {
			close(both)
		}
		select {
		case <-both:
		case <-r.Context().Done():
			return
		}
		data := video
		if r.URL.Path == "/audio" {
			data = audio
		}
		http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(data))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m, task, p := progressFixture(t, ctx, true)
	v := NewStreamDownloader([]string{server.URL + "/video"}, filepath.Join(t.TempDir(), "video"))
	a := NewStreamDownloader([]string{server.URL + "/audio"}, filepath.Join(t.TempDir(), "audio"))
	v.sizeHint = int64(len(video) + 8192)
	a.sizeHint = int64(len(audio) - 8192)
	p.setEstimatedSizes(v.sizeHint, a.sizeHint)
	v.SetTotalSizeCallback(func(n int64) { p.setSize(true, n) })
	a.SetTotalSizeCallback(func(n int64) { p.setSize(false, n) })
	if failure := downloadMediaTracks(ctx, v, a, 4, func(n int64) { p.record(n, true) }); failure != nil {
		t.Fatal(failure)
	}
	p.tick(p.lastTime.Add(time.Second))
	m.mu.RLock()
	defer m.mu.RUnlock()
	if task.TotalBytes != int64(len(video)+len(audio)) || task.DownloadedBytes != task.TotalBytes || task.Progress != 100 || strings.Contains(task.SizeStr, "约") {
		t.Fatalf("size hints did not become verified total/progress: %+v", task)
	}
	assertMediaBytes(t, v.targetPath, video)
	assertMediaBytes(t, a.targetPath, audio)
}

func TestProgressEstimateIsLabelledAndNeverCompletesMedia(t *testing.T) {
	ctx := context.Background()
	m, task, p := progressFixture(t, ctx, true)
	p.setSize(true, 0)
	p.setEstimatedSizes(1024*1024, 1024*1024)
	p.record(3*1024*1024, true) // an underestimated VBR stream can exceed its hint
	p.tick(p.lastTime.Add(time.Second))
	m.mu.RLock()
	if task.TotalBytes != 2*1024*1024 || !strings.Contains(task.SizeStr, "约") || task.Progress >= 100 {
		t.Errorf("estimated size looked like verified completion: %+v", task)
	}
	m.mu.RUnlock()
	p.setSize(true, 3*1024*1024)
	p.setSize(false, 1024*1024)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if task.TotalBytes != 4*1024*1024 || task.Progress != 75 || strings.Contains(task.SizeStr, "约") {
		t.Errorf("actual headers did not replace estimates: %+v", task)
	}
}

func TestVisibleTracksDownloadAndMergeWithoutFinalPlaceholder(t *testing.T) {
	fixture := t.TempDir()
	vSource, aSource := filepath.Join(fixture, "video.mp4"), filepath.Join(fixture, "audio.mp4")
	progressiveFixture(t, vSource, true, "")
	progressiveFixture(t, aSource, false, "")
	video, err := os.ReadFile(vSource)
	if err != nil {
		t.Fatal(err)
	}
	audio, err := os.ReadFile(aSource)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := video
		if r.URL.Path == "/audio" {
			data = audio
		}
		http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(data))
	}))
	defer server.Close()
	m := newTestManager(t)
	task := &DownloadTask{ID: newUUID(), Status: StatusPaused, OutputPath: filepath.Join(m.cfgMgr.Get().DownloadDir, "title [1080P].mp4")}
	m.tasks = []*DownloadTask{task}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.relocateTaskStreams(ctx, task); err != nil {
		t.Fatal(err)
	}
	v := NewStreamDownloader([]string{server.URL + "/video"}, task.VideoTmpPath)
	a := NewStreamDownloader([]string{server.URL + "/audio"}, task.AudioTmpPath)
	v.skipSizeProbe, a.skipSizeProbe = true, true
	if failure := downloadMediaTracks(ctx, v, a, 16, nil); failure != nil {
		t.Fatal(failure)
	}
	assertMediaBytes(t, task.VideoTmpPath, video)
	assertMediaBytes(t, task.AudioTmpPath, audio)
	if _, err := os.Stat(task.OutputPath); !os.IsNotExist(err) {
		t.Fatal("download published an unfinished MP4")
	}
	entries, err := os.ReadDir(filepath.Dir(task.OutputPath))
	if err != nil {
		t.Fatal(err)
	}
	var visible []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			visible = append(visible, e.Name())
		}
	}
	if len(visible) != 2 || !strings.HasSuffix(visible[0], ".downloading") || !strings.HasSuffix(visible[1], ".downloading") {
		t.Fatalf("16-worker download should have only two visible tracks: %v", visible)
	}
	if err := MergeAudioVideoContext(ctx, task.VideoTmpPath, task.AudioTmpPath, task.OutputPath, true); err != nil {
		t.Fatal(err)
	}
	// The manager performs this cleanup after a successful merge, including
	// its range checkpoints, while preserving the newly published MP4.
	cleanupStreamArtifacts(task.VideoTmpPath)
	cleanupStreamArtifacts(task.AudioTmpPath)
	assertProgressiveOutput(t, task.OutputPath)
	entries, err = os.ReadDir(filepath.Dir(task.OutputPath))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(task.OutputPath) {
		t.Fatalf("completion left unfinished artifacts: %v %v", entries, err)
	}
}
