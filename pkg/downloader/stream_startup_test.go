package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A CDN may reject/close the tiny Range probe while serving the ordinary
// download correctly. The optimisation must not be a mandatory download gate.
func TestStreamStartup_ProbeFailureFallsBackToVerifiedDownload(t *testing.T) {
	for _, failure := range []string{"EOF", "503", "400", "unknown-length"} {
		t.Run(failure, func(t *testing.T) {
			data := bytes.Repeat([]byte("verified media bytes"), 4096)
			var transfers atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") == "bytes=0-0" {
					switch failure {
					case "EOF":
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							_ = conn.Close()
						}
					case "503":
						w.WriteHeader(http.StatusServiceUnavailable)
					case "400":
						w.WriteHeader(http.StatusBadRequest)
					case "unknown-length":
						w.(http.Flusher).Flush()
					}
					return
				}
				transfers.Add(1)
				http.ServeContent(w, r, "media.mp4", time.Time{}, bytes.NewReader(data))
			}))
			defer ts.Close()
			target := filepath.Join(t.TempDir(), "video.tmp")
			dl := NewStreamDownloader([]string{ts.URL}, target)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := dl.DownloadWithConcurrency(ctx, 4, nil); err != nil {
				t.Fatalf("probe failure abandoned a downloadable stream: %v", err)
			}
			got, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(got, data) || dl.totalSize != int64(len(data)) {
				t.Fatalf("fallback did not verify complete media: bytes=%d total=%d err=%v", len(got), dl.totalSize, err)
			}
			if transfers.Load() != 1 {
				t.Fatalf("expected one transfer, got %d", transfers.Load())
			}
		})
	}
}

func TestStreamStartup_FallbackResumesAndRetriesConnectionErrors(t *testing.T) {
	data := bytes.Repeat([]byte("resume without duplicate bytes"), 2048)
	const prefix = 256
	var transfers atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" || transfers.Add(1) <= 2 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		if r.Header.Get("Range") != fmt.Sprintf("bytes=%d-", prefix) {
			t.Errorf("lost resumable prefix: %q", r.Header.Get("Range"))
		}
		http.ServeContent(w, r, "media.mp4", time.Time{}, bytes.NewReader(data))
	}))
	defer ts.Close()
	target := filepath.Join(t.TempDir(), "video.tmp")
	if err := os.WriteFile(target, data[:prefix], 0600); err != nil {
		t.Fatal(err)
	}
	dl := NewStreamDownloader([]string{ts.URL}, target)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var received int64
	if err := dl.DownloadWithConcurrency(ctx, 4, func(n int64) { received += n }); err != nil {
		t.Fatalf("transient connection errors were not recovered: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, data) || received != int64(len(data)-prefix) {
		t.Fatalf("resume corrupted media or progress: received=%d bytes=%d err=%v", received, len(got), err)
	}
}

func TestStreamStartup_CancelledProbeDoesNotStartFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int32
	dl := NewStreamDownloader([]string{"https://cdn.invalid/media"}, filepath.Join(t.TempDir(), "video.tmp"))
	dl.client.Transport = streamRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		cancel()
		return nil, io.EOF
	})
	err := dl.DownloadWithConcurrency(ctx, 4, nil)
	if !errors.Is(err, context.Canceled) || requests.Load() != 1 {
		t.Fatalf("cancelled probe must stop immediately: requests=%d err=%v", requests.Load(), err)
	}
}
