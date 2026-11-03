package downloader

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamAccelerationAddsWorkersOnAStablePlateau(t *testing.T) {
	data := engineData(2 * 1024 * 1024)
	var active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for n > peak.Load() {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		start, end := 0, len(data)-1
		if r.Header.Get("Range") != "" {
			_, _ = fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		}
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
			w.WriteHeader(206)
		}
		for start <= end {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Millisecond):
			}
			count := min(2048, end-start+1)
			if _, err := w.Write(data[start : start+count]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			start += count
		}
	}))
	defer server.Close()
	s := NewStreamDownloader([]string{server.URL}, filepath.Join(t.TempDir(), "media"))
	s.totalSize = int64(len(data))
	s.acceleration = &streamAccelerationPolicy{sample: 10 * time.Millisecond, settle: 40 * time.Millisecond, history: 3, addition: 3, maxWorkers: 4, stability: 1, requiredGain: 0.8, minRemaining: 64 * 1024}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.DownloadWithConcurrency(ctx, 1, nil); err != nil {
		t.Fatal(err)
	}
	assertMediaBytes(t, s.targetPath, data)
	if peak.Load() < 2 || peak.Load() > 4 {
		t.Fatalf("adaptive concurrency did not grow within its budget: %d", peak.Load())
	}
}

func TestStreamAccelerationStopsAfterConnectionsDoNotImproveSpeed(t *testing.T) {
	a := streamAccelerator{policy: defaultStreamAccelerationPolicy}
	now := time.Now()
	for i := 0; i < 4; i++ {
		if got := a.additional(now.Add(time.Duration(i)*time.Second), 100, 2, 100*1024*1024); got != 0 {
			t.Fatal("accelerated before a stable baseline")
		}
	}
	if got := a.additional(now.Add(4*time.Second), 100, 2, 100*1024*1024); got != 4 {
		t.Fatalf("plateau expansion=%d", got)
	}
	for i := 5; i < 20; i++ {
		if got := a.additional(now.Add(time.Duration(i)*time.Second), 100, 6, 100*1024*1024); got != 0 {
			t.Fatal("kept creating ineffective connections")
		}
	}
	if !a.spent {
		t.Fatal("failed acceleration did not stop further growth")
	}
}
