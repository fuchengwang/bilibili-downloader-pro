package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
	"bilibili_downloader/pkg/config"
)

func TestLiveManagerPerformanceAcceptance(t *testing.T) {
	if testing.Short() || os.Getenv("BBDOWN_PERFORMANCE_ACCEPTANCE") != "1" {
		t.Skip("opt-in full manager throughput/progress acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	fatal := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(diagnosticURLPattern.ReplaceAllString(err.Error(), "[hidden]"))
		}
	}
	client := bilibili.GetDefaultClient()
	target, err := client.ParseInput(ctx, "https://www.bilibili.com/video/BV1xx411c7mD/")
	fatal(err)
	detail, err := client.FetchVideoDetail(ctx, target)
	fatal(err)
	ep := detail.Episodes[0]
	sel, err := client.FetchStreamSelection(ctx, detail.BVID, detail.AID, ep.CID, 0, false, "32", "AVC")
	fatal(err)
	primary, err := url.Parse(sel.VideoURLs[0])
	fatal(err)
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, primary.Hostname())
	fatal(err)
	if len(addresses) == 0 {
		t.Fatal("no CDN IP")
	}
	address := addresses[0].IP.String()
	for _, candidate := range addresses {
		if candidate.IP.To4() != nil {
			address = candidate.IP.String()
			break
		}
	}
	if edge := os.Getenv("BBDOWN_PERFORMANCE_EDGE"); edge != "" {
		if net.ParseIP(edge) == nil {
			t.Fatal("invalid comparison edge IP")
		}
		address = edge
	}
	// Fix the endpoint for the comparison so DNS rotation cannot make one
	// client use a different edge. This affects this test's transport only.
	original := mediaTransport
	transport := mediaTransport.Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if host == primary.Hostname() {
			addr = net.JoinHostPort(address, port)
		}
		return (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, addr)
	}
	mediaTransport = transport
	defer func() { transport.CloseIdleConnections(); mediaTransport = original }()
	ref := map[string]any{"dialIP": address, "files": []any{map[string]any{"videoUrl": sel.VideoURLs[0], "headers": map[string]string{"referer": bilibili.Referer, "user-agent": bilibili.BrowserUA}}}}
	private, err := json.Marshal(ref)
	fatal(err)
	fatal(os.WriteFile("/tmp/bbdown-transport-reference.json", private, 0600))
	dir := os.Getenv("BBDOWN_PERFORMANCE_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	fatal(os.MkdirAll(dir, 0755))
	cfg := config.NewConfigManager(filepath.Join(dir, "state"))
	settings := cfg.Get()
	settings.DownloadDir = filepath.Join(dir, "output")
	settings.ThreadsPerTask = 4
	settings.DeleteTempFiles = false
	fatal(cfg.Save(settings))
	m := NewDownloadManager(cfg)
	m.streamFactory = func(urls []string, path string) *StreamDownloader {
		s := NewStreamDownloader(urls, path)
		base := s.client.Transport
		s.client.Transport = streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			resp, err := base.RoundTrip(r)
			if err == nil {
				t.Logf("track=%s host=%s request=%s status=%d response=%s bytes=%d", filepath.Base(path), r.URL.Hostname(), r.Header.Get("Range"), resp.StatusCode, resp.Header.Get("Content-Range"), resp.ContentLength)
			}
			return resp, err
		})
		s.onDiagnostic = func(err error) {
			t.Logf("track=%s error=%s", filepath.Base(path), diagnosticURLPattern.ReplaceAllString(err.Error(), "[hidden]"))
		}
		return s
	}
	defer func() {
		m.PauseAll()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			m.mu.RLock()
			n := len(m.workers)
			m.mu.RUnlock()
			if n == 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	updates := make(chan DownloadTask, 2048)
	m.SetCallback(func(task *DownloadTask) {
		select {
		case updates <- *task:
		default:
		}
	})
	tasks, err := m.AddDownloadTasks(&DownloadRequest{BVID: detail.BVID, AID: detail.AID, Title: "性能验收", TargetQuality: "32", TargetCodec: "AVC"}, []bilibili.EpisodeInfo{ep})
	fatal(err)
	if len(tasks) != 1 {
		t.Fatal("expected one isolated task")
	}
	started := time.Now()
	m.checkAndSpawnTasks()
	var firstByte, firstTotal, lastByte time.Time
	var firstBytes, lastBytes, peak int64
	var complete DownloadTask
	var samples []map[string]any
	for complete.Status != "completed" {
		select {
		case <-ctx.Done():
			m.PauseAll()
			t.Fatal(ctx.Err())
		case task := <-updates:
			now := time.Now()
			if task.Status == StatusError {
				t.Fatalf("manager failed: %s %s", task.ErrorMsg, task.ErrorDetail)
			}
			if task.Status == StatusDownloading {
				if task.DownloadedBytes > 0 && firstByte.IsZero() {
					firstByte = now
					firstBytes = task.DownloadedBytes
				}
				if task.TotalBytes > 0 && !strings.Contains(task.SizeStr, "约") && firstTotal.IsZero() {
					firstTotal = now
				}
				if task.DownloadedBytes > 0 && task.TotalBytes <= 0 {
					t.Fatal("payload arrived but total remained unknown despite parser estimates")
				}
				if task.DownloadedBytes >= 2*1024*1024 && firstTotal.IsZero() {
					t.Fatal("both real track lengths were still unknown after 2 MiB")
				}
				if task.DownloadedBytes > lastBytes {
					lastByte = now
					lastBytes = task.DownloadedBytes
				}
				peak = max(peak, task.Speed)
				samples = append(samples, map[string]any{"seconds": now.Sub(started).Seconds(), "bytes": task.DownloadedBytes, "total": task.TotalBytes, "estimated": strings.Contains(task.SizeStr, "约"), "speed": task.Speed})
			}
			if task.Status == StatusCompleted {
				complete = task
			}
		}
	}
	// Wait until the owning worker releases files and persists its final state.
	for {
		m.mu.RLock()
		running := len(m.workers)
		m.mu.RUnlock()
		if running == 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if firstByte.IsZero() || firstTotal.IsZero() {
		t.Fatal("missing live progress/confirmed size")
	}
	completedAt := time.Now()
	rate := float64(lastBytes-firstBytes) / lastByte.Sub(firstByte).Seconds() / 1024
	vHash, vBytes, err := hashFile(complete.VideoTmpPath)
	fatal(err)
	aHash, aBytes, err := hashFile(complete.AudioTmpPath)
	fatal(err)
	if complete.TotalBytes != vBytes+aBytes {
		t.Fatalf("final total mismatch: %d vs %d", complete.TotalBytes, vBytes+aBytes)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	fatal(err)
	probe, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "stream=codec_name,codec_type,width,height:format=duration", "-of", "json", complete.OutputPath).Output()
	fatal(err)
	ffmpeg, err := exec.LookPath("ffmpeg")
	fatal(err)
	decode, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-xerror", "-i", complete.OutputPath, "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("decode failed: %v %s", err, decode)
	}
	report := map[string]any{"bvid": detail.BVID, "cid": ep.CID, "quality": complete.QualityID, "codec": complete.Codec, "host": primary.Hostname(), "dialIP": address, "first_payload_seconds": firstByte.Sub(started).Seconds(), "confirmed_total_seconds": firstTotal.Sub(started).Seconds(), "elapsed_seconds": completedAt.Sub(started).Seconds(), "validation_seconds": time.Since(completedAt).Seconds(), "transfer_KiB_s": rate, "peak_display_KiB_s": float64(peak) / 1024, "video_bytes": vBytes, "audio_bytes": aBytes, "video_sha256": vHash, "audio_sha256": aHash, "decoded": true, "media": json.RawMessage(probe), "samples": samples}
	data, err := json.MarshalIndent(report, "", "  ")
	fatal(err)
	fatal(os.WriteFile(filepath.Join(dir, "manager-performance.json"), data, 0644))
	t.Logf("host=%s edge=%s quality=%d codec=%s first_payload=%.2fs confirmed_total=%.2fs transfer=%.1f KiB/s peak_display=%.1f KiB/s bytes=%d full_decode=PASS", primary.Hostname(), address, complete.QualityID, complete.Codec, firstByte.Sub(started).Seconds(), firstTotal.Sub(started).Seconds(), rate, float64(peak)/1024, vBytes+aBytes)
	// Throughput is assessed against the same-byte, same-edge Ghost reference,
	// never against a fixed rate on a currently constrained network.
	fmt.Println("performance report:", filepath.Join(dir, "manager-performance.json"))
}
