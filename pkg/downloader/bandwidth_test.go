package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bilibili_downloader/pkg/config"
)

func bandwidthServer(t *testing.T, data []byte, supportRange bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end := 0, len(data)-1
		partial := supportRange && r.Header.Get("Range") != ""
		if partial {
			parts := strings.Split(strings.TrimPrefix(r.Header.Get("Range"), "bytes="), "-")
			start, _ = strconv.Atoi(parts[0])
			if len(parts) > 1 && parts[1] != "" {
				end, _ = strconv.Atoi(parts[1])
			}
			if start < 0 || end >= len(data) || start > end {
				http.Error(w, "bad range", 416)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		if partial {
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = w.Write(data[start : end+1])
	}))
	t.Cleanup(server.Close)
	return server
}

func assertMediaBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("download corrupted: got %d bytes, want %d", len(got), len(want))
	}
}

func TestBandwidthAggregateAcrossTasksAndRangeWorkers(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 8192)
	server := bandwidthServer(t, data, true)
	const rate = 256 * 1024
	limiter := &bandwidthLimiter{limit: func() int64 { return rate }}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 2)
	start := time.Now()
	var total atomic.Int64
	paths := make([]string, 2)
	for i, threads := range []int{1, 4} {
		paths[i] = filepath.Join(t.TempDir(), "media")
		dl := NewStreamDownloader([]string{server.URL}, paths[i])
		dl.bandwidth = limiter
		go func() { done <- dl.DownloadWithConcurrency(ctx, threads, func(n int64) { total.Add(n) }) }()
	}
	for range paths {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	if elapsed < 900*time.Millisecond {
		t.Fatalf("aggregate cap bypassed: %d bytes in %v", total.Load(), elapsed)
	}
	if total.Load() != int64(2*len(data)) {
		t.Fatalf("progress double counted: %d", total.Load())
	}
	for _, path := range paths {
		assertMediaBytes(t, path, data)
	}
	t.Logf("two tasks (single + 4 ranges): %d bytes in %v", total.Load(), elapsed)
}

func TestBandwidthRangeFallbackAndResumePreserveContent(t *testing.T) {
	data := bytes.Repeat([]byte("media-data"), 4096)
	for _, supportRange := range []bool{true, false} {
		t.Run(fmt.Sprint(supportRange), func(t *testing.T) {
			server := bandwidthServer(t, data, supportRange)
			path := filepath.Join(t.TempDir(), "resume")
			if err := os.WriteFile(path, data[:len(data)/3], 0600); err != nil {
				t.Fatal(err)
			}
			dl := NewStreamDownloader([]string{server.URL}, path)
			dl.bandwidth = &bandwidthLimiter{limit: func() int64 { return 128 * 1024 }}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := dl.DownloadWithConcurrency(ctx, 4, nil); err != nil {
				t.Fatal(err)
			}
			assertMediaBytes(t, path, data)
		})
	}
}

func TestBandwidthCancelAndRemoveLimitDuringDownload(t *testing.T) {
	data := bytes.Repeat([]byte("media"), 32768)
	server := bandwidthServer(t, data, true)
	for _, threads := range []int{1, 4} {
		t.Run(fmt.Sprint(threads), func(t *testing.T) {
			cfg := config.NewConfigManager(t.TempDir())
			s := cfg.Get()
			s.DownloadLimitKB = 1
			if err := cfg.Save(s); err != nil {
				t.Fatal(err)
			}
			manager := NewDownloadManager(cfg)
			path := filepath.Join(t.TempDir(), "media")
			dl := NewStreamDownloader([]string{server.URL}, path)
			dl.bandwidth = manager.bandwidth
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			progress := make(chan struct{}, 1)
			go func() {
				done <- dl.DownloadWithConcurrency(ctx, threads, func(n int64) {
					if n > 0 {
						select {
						case progress <- struct{}{}:
						default:
						}
					}
				})
			}()
			select {
			case <-progress:
			case <-time.After(2 * time.Second):
				cancel()
				t.Fatal("limited download did not make progress")
			}
			start := time.Now()
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel returned %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancel stuck in throttling wait")
			}
			if time.Since(start) > 500*time.Millisecond {
				t.Fatal("cancel was too slow")
			}
			// Resume at the same low limit, then turn it off while reads are
			// waiting. This exercises the exact manager/config shared bucket.
			dl = NewStreamDownloader([]string{server.URL}, path)
			dl.bandwidth = manager.bandwidth
			ctx, stop := context.WithTimeout(context.Background(), 4*time.Second)
			defer stop()
			go func() { done <- dl.DownloadWithConcurrency(ctx, threads, nil) }()
			time.Sleep(150 * time.Millisecond)
			s.DownloadLimitKB = 0
			if err := cfg.Save(s); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("removing the limit left old waits or quota debt")
			}
			assertMediaBytes(t, path, data)
		})
	}
}

func TestBandwidthLiveRateChangesAndUnlimited(t *testing.T) {
	var rate atomic.Int64
	limiter := &bandwidthLimiter{limit: rate.Load}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if n, err := limiter.acquire(ctx, 128*1024); err != nil || n != 128*1024 {
		t.Fatal("default unlimited path changed")
	}
	for _, limit := range []int64{1024, 4096, 512} {
		rate.Store(limit)
		start := time.Now()
		n, err := limiter.acquire(ctx, 128*1024)
		if err != nil || int64(n) > limit/10 || time.Since(start) < 80*time.Millisecond {
			t.Fatalf("live limit %d was not applied: grant=%d elapsed=%v err=%v", limit, n, time.Since(start), err)
		}
	}
	rate.Store(0)
	if n, err := limiter.acquire(ctx, 128*1024); err != nil || n != 128*1024 {
		t.Fatal("unlimited did not discard old limit")
	}
}

func TestBandwidthTruncatedResponseRetriesWithoutCorruption(t *testing.T) {
	data := bytes.Repeat([]byte("media"), 8192)
	const firstSize = 4096
	for _, segmented := range []bool{false, true} {
		t.Run(fmt.Sprint(segmented), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				start, end := 0, len(data)-1
				if header := req.Header.Get("Range"); header != "" {
					parts := strings.Split(strings.TrimPrefix(header, "bytes="), "-")
					start, _ = strconv.Atoi(parts[0])
					if parts[1] != "" {
						end, _ = strconv.Atoi(parts[1])
					}
				}
				attempt := requests.Add(1)
				if attempt == 2 && start != firstSize {
					t.Errorf("retry started at %d, want %d", start, firstSize)
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
				w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
				w.WriteHeader(http.StatusPartialContent)
				if attempt == 1 {
					_, _ = w.Write(data[:firstSize])
					return
				}
				_, _ = w.Write(data[start : end+1])
			}))
			defer server.Close()
			dl := NewStreamDownloader([]string{server.URL}, filepath.Join(t.TempDir(), "retry"))
			dl.bandwidth = &bandwidthLimiter{limit: func() int64 { return 128 * 1024 }}
			dl.totalSize = int64(len(data))
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			path := dl.targetPath
			var progress int64
			report := func(n int64) { progress += n }
			var err error
			if segmented {
				path = streamSegmentPath(dl.targetPath, 0)
				segment := streamSegment{start: 0, end: int64(len(data) - 1), path: path}
				err = dl.downloadSegment(ctx, &segment, report)
			} else {
				err = dl.DownloadSingleStream(ctx, report)
			}
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 2 || progress != int64(len(data)) {
				t.Fatalf("retry/progress mismatch: requests=%d bytes=%d", requests.Load(), progress)
			}
			assertMediaBytes(t, path, data)
		})
	}
}

func TestBandwidthSlowSharedStreamsDoNotTriggerNetworkIdleTimeout(t *testing.T) {
	// With 90 readers sharing 1 KiB/s, an individual reader can wait over
	// the CDN's 8-second timeout. These waits must stay outside that timer.
	if testing.Short() {
		t.Skip("10-second real-time slow bandwidth regression")
	}
	limiter := &bandwidthLimiter{limit: func() int64 { return 1024 }}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	done := make(chan error, 90)
	for range 90 {
		dl := NewStreamDownloader(nil, filepath.Join(t.TempDir(), "slow"))
		dl.bandwidth = limiter
		go func() { done <- dl.streamToFile(ctx, bytes.NewReader(bytes.Repeat([]byte("x"), 102)), false, nil) }()
	}
	start := time.Now()
	for range 90 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) < 8*time.Second {
		t.Fatal("test did not exercise a wait longer than network idle timeout")
	}
}
