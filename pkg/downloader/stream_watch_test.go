package downloader

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestSlowRecoveryExclusions(t *testing.T) {
	for _, name := range []string{"limited", "non-range", "unknown-size", "near-end", "budget-spent"} {
		t.Run(name, func(t *testing.T) {
			dl := NewStreamDownloader(nil, "unused")
			size, resumable := int64(1024*1024), true
			switch name {
			case "limited":
				dl.bandwidth = &bandwidthLimiter{limit: func() int64 { return 1024 }}
			case "non-range":
				resumable = false
			case "unknown-size":
				size = -1
			case "near-end":
				size = 10
			case "budget-spent":
				dl.slowRecoveries.Store(3)
			}
			body := dl.watchResponse(io.NopCloser(bytes.NewReader([]byte("ok"))), size, resumable).(*watchedStreamBody)
			// Simulate a prior slow window without making boundary tests sleep.
			body.elapsed = time.Minute
			if _, err := body.Read(make([]byte, 2)); err != nil {
				t.Fatal(err)
			}
			want := int32(0)
			if name == "budget-spent" {
				want = 3
			}
			if dl.slowRecoveries.Load() != want {
				t.Fatal("unexpected recovery")
			}
		})
	}
}

// A CDN that keeps sending data never hits the idle timeout. Recovery must
// retain exactly the received prefix and request the remainder from backup.
func TestSlowStreamRecoveryPreservesBytes(t *testing.T) {
	for _, threads := range []int{1, 4} {
		t.Run(fmt.Sprint(threads), func(t *testing.T) {
			data := make([]byte, 512*1024)
			for i := range data {
				data[i] = byte(i*31 + i/127)
			}
			var backups atomic.Int32
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backups.Add(1)
				http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(data))
			}))
			defer backup.Close()
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				start, end := 0, len(data)-1
				if r.Header.Get("Range") != "" {
					_, _ = fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Accept-Ranges", "bytes")
				w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
				if r.Header.Get("Range") != "" {
					w.WriteHeader(206)
				}
				for start <= end {
					select {
					case <-r.Context().Done():
						return
					case <-time.After(5 * time.Millisecond):
					}
					n := min(1024, end-start+1)
					if _, err := w.Write(data[start : start+n]); err != nil {
						return
					}
					w.(http.Flusher).Flush()
					start += n
				}
			}))
			defer primary.Close()
			path := filepath.Join(t.TempDir(), "media")
			dl := NewStreamDownloader([]string{primary.URL, backup.URL}, path)
			dl.totalSize = int64(len(data))
			dl.readPolicy = &streamReadPolicy{idle: time.Second, window: 25 * time.Millisecond, minRate: 1024 * 1024, minRemaining: 1}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var progress atomic.Int64
			if err := dl.DownloadWithConcurrency(ctx, threads, func(n int64) { progress.Add(n) }); err != nil {
				t.Fatal(err)
			}
			assertMediaBytes(t, path, data)
			if backups.Load() == 0 {
				t.Fatal("backup was never used")
			}
			if progress.Load() != int64(len(data)) {
				t.Fatalf("progress=%d", progress.Load())
			}
			if dl.slowRecoveries.Load() > 3 {
				t.Fatal("recovery budget exceeded")
			}
		})
	}
}

func TestSegmentIdleRecoveryAndCancellation(t *testing.T) {
	data := bytes.Repeat([]byte("media"), 8192)
	backup := bandwidthServer(t, data, true)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end := 0, len(data)-1
		_, _ = fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.WriteHeader(206)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer primary.Close()
	for _, cancelEarly := range []bool{false, true} {
		dl := NewStreamDownloader([]string{primary.URL, backup.URL}, filepath.Join(t.TempDir(), "media"))
		dl.totalSize = int64(len(data))
		dl.readPolicy = &streamReadPolicy{idle: 30 * time.Millisecond, window: time.Minute}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if cancelEarly {
			time.AfterFunc(10*time.Millisecond, cancel)
		}
		err := dl.DownloadWithConcurrency(ctx, 2, nil)
		cancel()
		if cancelEarly {
			if err != context.Canceled {
				t.Fatalf("cancel returned %v", err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			assertMediaBytes(t, dl.targetPath, data)
		}
	}
}
