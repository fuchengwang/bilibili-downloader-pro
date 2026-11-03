package downloader

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
	"bilibili_downloader/pkg/config"
)

// An isolated diagnostic of the reported representation and the application's
// inherited proxy route. Never modifies the user's queue or network settings.
func TestLiveReportedConnectionFailure(t *testing.T) {
	if testing.Short() || os.Getenv("BBDOWN_REPORTED_FAILURE") != "1" {
		t.Skip("opt-in reported connection failure")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sel, err := bilibili.GetDefaultClient().FetchStreamSelection(ctx, "BV1ZL4c6NEmM", 117544748849271, 43254285984, 0, false, "116", "AVC")
	if err != nil {
		t.Fatal(diagnosticURLPattern.ReplaceAllString(err.Error(), "[hidden]"))
	}
	private, _ := json.Marshal(sel)
	if err := os.WriteFile("/tmp/bbdown-reported-selection.json", private, 0600); err != nil {
		t.Fatal(err)
	}
	for _, urls := range [][]string{sel.VideoURLs, sel.AudioURLs} {
		for _, raw := range urls {
			u, _ := url.Parse(raw)
			for _, direct := range []bool{false, true} {
				tr := mediaTransport.Clone()
				if direct {
					tr.Proxy = nil
				}
				for attempt := 0; attempt < 2; attempt++ {
					run, stop := context.WithTimeout(ctx, 6*time.Second)
					req, _ := http.NewRequestWithContext(run, "GET", raw, nil)
					setMediaHeaders(req)
					req.Close = true
					req.Header.Set("Range", "bytes=0-262143")
					started := time.Now()
					resp, err := (&http.Client{Transport: tr}).Do(req)
					if err != nil {
						t.Logf("host=%s direct=%v attempt=%d elapsed=%v error=%s", u.Hostname(), direct, attempt, time.Since(started), diagnosticURLPattern.ReplaceAllString(err.Error(), "[hidden]"))
					} else {
						n, bodyErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 262144))
						resp.Body.Close()
						t.Logf("host=%s direct=%v attempt=%d status=%d protocol=%s bytes=%d elapsed=%v bodyError=%v", u.Hostname(), direct, attempt, resp.StatusCode, resp.Proto, n, time.Since(started), bodyErr)
					}
					stop()
				}
				tr.CloseIdleConnections()
			}
		}
	}
}

// End-to-end acceptance deliberately keeps the inherited proxy environment.
// The manager, range workers and route recovery all run as in the desktop app.
func TestLiveReportedDownloadAcceptance(t *testing.T) {
	if testing.Short() || os.Getenv("BBDOWN_REPORTED_ACCEPTANCE") != "1" {
		t.Skip("opt-in full reported representation download")
	}
	dir := os.Getenv("BBDOWN_REPORTED_OUTPUT")
	if dir == "" {
		t.Fatal("isolated output directory required")
	}
	probe, _ := http.NewRequest("GET", "https://upos-sz-mirrorhwo1.bilivideo.com/", nil)
	proxy, proxyErr := mediaTransport.Proxy(probe)
	if proxyErr != nil || proxy == nil {
		t.Fatal("this acceptance must preserve the inherited proxy environment")
	}
	cfg := config.NewConfigManager(filepath.Join(dir, "state"))
	settings := cfg.Get()
	settings.DownloadDir = filepath.Join(dir, "output")
	settings.ThreadsPerTask = 4
	settings.DeleteTempFiles = true
	if err := cfg.Save(settings); err != nil {
		t.Fatal(err)
	}
	m := NewDownloadManager(cfg)
	updates := make(chan DownloadTask, 4096)
	m.SetCallback(func(task *DownloadTask) {
		select {
		case updates <- *task:
		default:
		}
	})
	tasks, err := m.AddDownloadTasks(&DownloadRequest{BVID: "BV1ZL4c6NEmM", AID: 117544748849271, Title: "连接恢复验收", TargetQuality: "highest", TargetCodec: "auto"}, []bilibili.EpisodeInfo{{BVID: "BV1ZL4c6NEmM", AID: 117544748849271, CID: 43254285984, Title: "连接恢复验收"}})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("prepare task: %v", err)
	}
	defer func() {
		m.PauseAll()
		for {
			m.mu.RLock()
			n := len(m.workers)
			m.mu.RUnlock()
			if n == 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	started := time.Now()
	m.checkAndSpawnTasks()
	var completed DownloadTask
	lastReport := time.Time{}
	for completed.Status != StatusCompleted {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case task := <-updates:
			if task.Status == StatusError {
				t.Fatalf("manager failed: %s %s", task.ErrorMsg, task.ErrorDetail)
			}
			if time.Since(lastReport) > 10*time.Second || task.Status == StatusCompleted {
				t.Logf("status=%s received=%d total=%d elapsed=%.2fs", task.Status, task.DownloadedBytes, task.TotalBytes, time.Since(started).Seconds())
				lastReport = time.Now()
			}
			if task.Status == StatusCompleted {
				completed = task
			}
		}
	}
	transferSeconds := time.Since(started).Seconds()
	sharedMediaRoutes.mu.Lock()
	recoveredHosts := len(sharedMediaRoutes.directUntil)
	sharedMediaRoutes.mu.Unlock()
	if recoveredHosts == 0 {
		t.Fatal("download did not exercise proxy-to-direct recovery")
	}
	decode := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-xerror", "-i", completed.OutputPath, "-f", "null", "-")
	if out, err := decode.CombinedOutput(); err != nil {
		t.Fatalf("full decode failed: %v %s", err, out)
	}
	sha, size, err := hashFile(completed.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"bvid": completed.BVID, "quality": completed.QualityLabel, "codec": completed.Codec, "network_bytes": completed.TotalBytes, "elapsed_seconds": transferSeconds, "output_bytes": size, "output_sha256": sha, "full_decode": "PASS", "inherited_proxy_preserved": true, "recovered_route_hosts": recoveredHosts}
	data, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "acceptance.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
}
