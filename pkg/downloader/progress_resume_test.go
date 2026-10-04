package downloader

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestResumeReportsCachedPartsWithoutNetworkTraffic(t *testing.T) {
	for _, threads := range []int{4, 8} {
		t.Run(fmt.Sprintf("threads-%d", threads), func(t *testing.T) {
			const total = 4 * 1024 * 1024
			const savedPerPart = 128 * 1024
			requests := make(chan struct{}, threads)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- struct{}{}
				<-r.Context().Done() // No media bytes have been sent.
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			target := filepath.Join(t.TempDir(), "video.downloading")
			for part := 0; part < threads; part++ {
				if err := os.WriteFile(streamSegmentPath(target, part), bytes.Repeat([]byte{0x42}, savedPerPart), 0644); err != nil {
					t.Fatal(err)
				}
			}
			dl := NewStreamDownloader([]string{server.URL}, target)
			dl.totalSize = total
			var restored, network int64
			dl.SetRestoredProgressCallback(func(delta int64) { atomic.AddInt64(&restored, delta) })
			done := make(chan error, 1)
			go func() {
				done <- dl.DownloadWithConcurrency(ctx, threads, func(delta int64) { atomic.AddInt64(&network, delta) })
			}()
			for part := 0; part < threads; part++ {
				select {
				case <-requests:
				case <-time.After(3 * time.Second):
					t.Fatal("range request did not start")
				}
			}
			cancel()
			if err := <-done; err != context.Canceled {
				t.Fatalf("cancellation: %v", err)
			}
			if got := atomic.LoadInt64(&network); got != 0 {
				t.Errorf("no network payload sent, but progress reported %d newly transferred bytes", got)
			}
			if got, want := atomic.LoadInt64(&restored), int64(threads*savedPerPart); got != want {
				t.Errorf("restored bytes = %d, want %d", got, want)
			}
		})
	}
}

func TestRepeatedPauseResumeCountsEachByteOnce(t *testing.T) {
	for _, threads := range []int{1, 4, 8} {
		t.Run(fmt.Sprintf("threads-%d", threads), func(t *testing.T) {
			data := bytes.Repeat([]byte("video"), 128*1024)
			var round atomic.Int32
			firstChunks := make(chan struct{}, threads)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				start, end := 0, len(data)-1
				if header := r.Header.Get("Range"); header != "" {
					if _, err := fmt.Sscanf(header, "bytes=%d-%d", &start, &end); err != nil {
						if _, err := fmt.Sscanf(header, "bytes=%d-", &start); err != nil {
							http.Error(w, "range", 400)
							return
						}
					}
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
				}
				w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
				if r.Header.Get("Range") != "" {
					w.WriteHeader(http.StatusPartialContent)
				}
				if round.Load() < 3 {
					chunkEnd := start + 4096
					_, _ = w.Write(data[start:chunkEnd])
					w.(http.Flusher).Flush()
					select {
					case firstChunks <- struct{}{}:
					case <-r.Context().Done():
						return
					}
					<-r.Context().Done()
				} else {
					_, _ = w.Write(data[start : end+1])
				}
			}))
			defer server.Close()
			target := filepath.Join(t.TempDir(), "video.downloading")
			var previous int64
			for attempt := 0; attempt < 4; attempt++ {
				round.Store(int32(attempt))
				ctx, cancel := context.WithCancel(context.Background())
				dl := NewStreamDownloader([]string{server.URL}, target)
				dl.totalSize = int64(len(data))
				var restored, network int64
				if info, err := os.Stat(target); err == nil {
					restored = info.Size()
				}
				dl.SetRestoredProgressCallback(func(delta int64) { atomic.AddInt64(&restored, delta) })
				done := make(chan error, 1)
				go func() {
					done <- dl.DownloadWithConcurrency(ctx, threads, func(delta int64) { atomic.AddInt64(&network, delta) })
				}()
				if attempt < 3 {
					deadline := time.After(3 * time.Second)
					for atomic.LoadInt64(&network) < int64(threads*4096) {
						select {
						case <-firstChunks:
						case <-time.After(5 * time.Millisecond):
						case <-deadline:
							cancel()
							t.Fatal("no live progress")
						}
					}
					cancel()
				}
				err := <-done
				cancel()
				if attempt < 3 && err != context.Canceled {
					t.Fatalf("pause: %v", err)
				}
				if attempt == 3 && err != nil {
					t.Fatal(err)
				}
				if got := atomic.LoadInt64(&restored); got != previous {
					t.Fatalf("resume counted saved bytes twice: got %d, want %d", got, previous)
				}
				previous = atomic.LoadInt64(&restored) + atomic.LoadInt64(&network)
				if attempt < 3 && previous != int64((attempt+1)*threads*4096) {
					t.Fatalf("progress %d", previous)
				}
			}
			if previous != int64(len(data)) {
				t.Fatalf("final bytes %d", previous)
			}
			actual, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(actual, data) {
				t.Fatalf("media corrupt: %v", err)
			}
		})
	}
}

func TestManagerReportsLiveProgressWhileRangesAreUnfinished(t *testing.T) {
	const threads = 4
	const total = 4 * 1024 * 1024
	const savedPerPart = 128 * 1024
	const sentPerPart = 32 * 1024
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			http.Error(w, "range", 400)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(bytes.Repeat([]byte{0x42}, sentPerPart))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // No response finishes before a real manager update.
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m, task, _ := progressFixture(t, ctx, false)
	target := filepath.Join(t.TempDir(), "video.downloading")
	task.VideoTmpPath = target
	for part := 0; part < threads; part++ {
		if err := os.WriteFile(streamSegmentPath(target, part), bytes.Repeat([]byte{0x42}, savedPerPart), 0644); err != nil {
			t.Fatal(err)
		}
	}
	p := newTaskDownloadProgress(ctx, m, task, "worker-1", total, 0, false)
	dl := NewStreamDownloader([]string{server.URL}, target)
	dl.totalSize = total
	dl.SetRestoredProgressCallback(func(delta int64) { p.record(delta, false) })
	updates := make(chan DownloadTask, 8)
	m.SetCallback(func(task *DownloadTask) {
		select {
		case updates <- *task:
		default:
		}
	})
	stop := p.start()
	defer stop()
	done := make(chan error, 1)
	go func() { done <- dl.DownloadWithConcurrency(ctx, threads, func(delta int64) { p.record(delta, true) }) }()
	ended := false
	select {
	case update := <-updates:
		if want := int64(threads * (savedPerPart + sentPerPart)); update.DownloadedBytes != want {
			t.Errorf("visible bytes %d, want %d", update.DownloadedBytes, want)
		}
		if update.Progress != 15.625 {
			t.Errorf("visible progress %.4f%%", update.Progress)
		}
		// Existing 512 KiB must not contribute to speed. Only 128 KiB was sent.
		if update.Speed <= 0 || update.Speed > int64(threads*sentPerPart)*2 {
			t.Errorf("speed counted cached bytes: %d B/s", update.Speed)
		}
	case err := <-done:
		ended = true
		t.Errorf("ended before live progress: %v", err)
	case <-time.After(3 * time.Second):
		t.Error("no visible progress while receiving media")
	}
	cancel()
	if !ended {
		if err := <-done; err != context.Canceled {
			t.Fatalf("pause: %v", err)
		}
	}
}

func TestProgressLearnsTotalFromDownloadAfterProbeFailure(t *testing.T) {
	data := bytes.Repeat([]byte{0x42}, 32*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	}))
	defer server.Close()
	ctx := context.Background()
	m, task, _ := progressFixture(t, ctx, false)
	task.VideoTmpPath = filepath.Join(t.TempDir(), "video.downloading")
	p := newTaskDownloadProgress(ctx, m, task, "worker-1", 0, 0, false)
	dl := NewStreamDownloader([]string{server.URL}, task.VideoTmpPath)
	dl.SetTotalSizeCallback(func(size int64) { p.setSize(true, size) })
	if err := dl.DownloadSingleStream(ctx, func(delta int64) { p.record(delta, true) }); err != nil {
		t.Fatal(err)
	}
	p.tick(p.lastTime.Add(time.Second))
	if task.TotalBytes != int64(len(data)) || task.DownloadedBytes != int64(len(data)) || task.Progress != 100 {
		t.Fatalf("download knows its size but UI remained at 0%%: %+v", task)
	}
}
