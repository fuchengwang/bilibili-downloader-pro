package downloader

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Use the identical private URL and pinned edge as the official HttpTaskStep
// harness. No metadata request, user task queue, or cached media is reused.
func TestLiveDirectGhostReference(t *testing.T) {
	if testing.Short() || os.Getenv("BBDOWN_DIRECT_GHOST_REFERENCE") != "1" {
		t.Skip("opt-in identical-byte official Ghost comparison")
	}
	var ref struct {
		DialIP string `json:"dialIP"`
		Files  []struct {
			VideoURL string `json:"videoUrl"`
		} `json:"files"`
	}
	private, err := os.ReadFile("/tmp/bbdown-transport-reference.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(private, &ref); err != nil {
		t.Fatal(err)
	}
	if len(ref.Files) == 0 {
		t.Fatal("missing reference")
	}
	u, err := url.Parse(ref.Files[0].VideoURL)
	if err != nil {
		t.Fatal("invalid reference")
	}
	dir := os.Getenv("BBDOWN_REFERENCE_DIR")
	if dir == "" {
		t.Fatal("missing isolated reference output directory")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	transport := mediaTransport.Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if host == u.Hostname() && ref.DialIP != "" {
			addr = net.JoinHostPort(ref.DialIP, port)
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	}
	defer transport.CloseIdleConnections()
	s := NewStreamDownloader([]string{ref.Files[0].VideoURL}, filepath.Join(dir, "go-reference-video.m4s"))
	s.client.Transport = transport
	s.skipSizeProbe = true
	s.sizeHint = 40_030_956 // estimate only; actual Content-Range must verify it
	threads, _ := strconv.Atoi(os.Getenv("BBDOWN_REFERENCE_THREADS"))
	if threads <= 0 {
		threads = 8
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	started := time.Now()
	var mu sync.Mutex
	var first, last time.Time
	var received int64
	err = s.DownloadWithConcurrency(ctx, threads, func(n int64) {
		mu.Lock()
		defer mu.Unlock()
		if first.IsZero() {
			first = time.Now()
		}
		last = time.Now()
		received += n
	})
	if err != nil {
		t.Fatal(diagnosticURLPattern.ReplaceAllString(err.Error(), "[hidden]"))
	}
	sha, size, err := hashFile(s.targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if size != s.totalSize || received != size {
		t.Fatalf("wrong total: disk=%d actual=%d received=%d", size, s.totalSize, received)
	}
	rate := float64(size) / last.Sub(first).Seconds() / 1024
	report := map[string]any{"host": u.Hostname(), "dialIP": ref.DialIP, "threads": threads, "bytes": size, "sha256": sha, "transfer_KiB_s": rate, "elapsed_seconds": time.Since(started).Seconds(), "first_payload_seconds": first.Sub(started).Seconds()}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go-reference.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	t.Log(string(data))
}
