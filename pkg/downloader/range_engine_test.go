package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

func copyTestBody(ctx context.Context, s *StreamDownloader, body io.ReadCloser, total int64) error {
	f, err := os.Create(s.targetPath)
	if err != nil {
		return err
	}
	defer f.Close()
	defer body.Close()
	b := &byteRange{End: total}
	e := &rangeTransfer{s: s, file: f, total: total}
	return e.copyResponse(ctx, &http.Response{Body: body, ContentLength: total}, b)
}

func TestRangeEngineTransientFullResponseDoesNotRestartVerifiedTransfer(t *testing.T) {
	data := engineData(8 * 1024 * 1024)
	var requests, full atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		w.Header().Set("ETag", `"stable"`)
		if r.Header.Get("Range") == "" {
			full.Add(1)
		}
		if n == 2 {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(200)
			return
		}
		http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(data))
	}))
	defer server.Close()
	s := NewStreamDownloader([]string{server.URL}, filepath.Join(t.TempDir(), "media"))
	s.totalSize = int64(len(data))
	var progress int64
	if err := s.DownloadWithConcurrency(context.Background(), 4, func(n int64) { progress += n }); err != nil {
		t.Fatal(err)
	}
	assertMediaBytes(t, s.targetPath, data)
	if full.Load() != 0 || progress != int64(len(data)) {
		t.Fatalf("transient 200 reset good ranges: full=%d progress=%d", full.Load(), progress)
	}
}

func TestRangeEngineValidatorsAreScopedToCDNSource(t *testing.T) {
	data := engineData(4 * 1024 * 1024)
	var primaryCount, backupCount atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if primaryCount.Add(1) > 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("ETag", `"primary-tag"`)
		http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(data))
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCount.Add(1)
		if r.Header.Get("If-Range") != "" {
			t.Error("a validator from another CDN was sent to this origin")
		}
		w.Header().Set("ETag", `"backup-tag"`)
		http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(data))
	}))
	defer backup.Close()
	s := NewStreamDownloader([]string{primary.URL, backup.URL}, filepath.Join(t.TempDir(), "media"))
	s.totalSize = int64(len(data))
	var progress int64
	if err := s.DownloadWithConcurrency(context.Background(), 4, func(n int64) { progress += n }); err != nil {
		t.Fatal(err)
	}
	assertMediaBytes(t, s.targetPath, data)
	if backupCount.Load() == 0 || progress != int64(len(data)) {
		t.Fatalf("cross-CDN failover restarted good bytes: backup=%d progress=%d", backupCount.Load(), progress)
	}
}

func TestSourcePoolReturnsToPreferredSourceAfterCooldown(t *testing.T) {
	p := newSourcePool([]string{"https://preferred.example/media", "https://backup.example/media"}, nil)
	p.failed("https://preferred.example/media", time.Hour)
	if got, _ := p.next(context.Background()); got != "https://backup.example/media" {
		t.Fatal(got)
	}
	p.mu.Lock()
	p.sources[0].ready = time.Now().Add(-time.Second)
	p.mu.Unlock()
	if got, _ := p.next(context.Background()); got != "https://preferred.example/media" {
		t.Fatal("recovered preferred source lost to the zero-time backup:", got)
	}
}

type countedResponseBody struct {
	io.ReadCloser
	read   int64
	closed func(int64)
}

func (b *countedResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.read += int64(n)
	return n, err
}

func (b *countedResponseBody) Close() error {
	b.closed(b.read)
	return b.ReadCloser.Close()
}

func TestRangeEngineBoundsConsumptionWhenCDNIgnoresRangeEnd(t *testing.T) {
	data := engineData(12 * 1024 * 1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		// A real CDN can honor only the start, returning the whole tail.
		w.Header().Set("Content-Length", strconv.Itoa(len(data)-start))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
		w.WriteHeader(206)
		_, _ = w.Write(data[start:])
	}))
	defer server.Close()
	s := NewStreamDownloader([]string{server.URL}, filepath.Join(t.TempDir(), "media"))
	s.sizeHint = int64(len(data))
	s.skipSizeProbe = true
	base := s.client.Transport
	var peak atomic.Int64
	s.client.Transport = streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(r)
		if err == nil {
			resp.Body = &countedResponseBody{ReadCloser: resp.Body, closed: func(n int64) {
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
			}}
		}
		return resp, err
	})
	if err := s.DownloadWithConcurrency(context.Background(), 2, nil); err != nil {
		t.Fatal(err)
	}
	assertMediaBytes(t, s.targetPath, data)
	if peak.Load() > maximumRequestSize {
		t.Fatalf("CDN bypassed request renewal: consumed=%d", peak.Load())
	}
}

func seedRangeRecord(t *testing.T, path string, total int64, threads int, perRange int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(total); err != nil {
		t.Fatal(err)
	}
	r := rangeRecord{Version: 1, Total: total}
	for i := 0; i < threads; i++ {
		start := total * int64(i) / int64(threads)
		end := total * int64(i+1) / int64(threads)
		if _, err := f.WriteAt(bytes.Repeat([]byte{0x42}, int(perRange)), start); err != nil {
			t.Fatal(err)
		}
		r.Ranges = append(r.Ranges, byteRange{Start: start, Position: start + perRange, End: end})
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writeRangeRecord(path, r); err != nil {
		t.Fatal(err)
	}
}

func engineData(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*31 + i/127)
	}
	return data
}

func TestRangeEngineStealsSlowTailAndWritesOneFile(t *testing.T) {
	data := engineData(4 * 1024 * 1024)
	var requests, active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("range bytes may not be compressed")
		}
		n := active.Add(1)
		defer active.Add(-1)
		for n > peak.Load() {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		request := requests.Add(1)
		var start, end int
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.WriteHeader(206)
		if request == 1 {
			for start <= end {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(time.Millisecond):
				}
				count := min(4096, end-start+1)
				if _, err := w.Write(data[start : start+count]); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				start += count
			}
		} else {
			_, _ = w.Write(data[start : end+1])
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "media")
	s := NewStreamDownloader([]string{server.URL}, path)
	s.totalSize = int64(len(data))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var progress atomic.Int64
	if err := s.DownloadWithConcurrency(ctx, 4, func(n int64) { progress.Add(n) }); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(actual) != sha256.Sum256(data) {
		t.Fatal("direct-write media hash mismatch", err)
	}
	if requests.Load() <= 4 {
		t.Fatal("idle workers never took the slow worker's tail")
	}
	if peak.Load() < 2 || peak.Load() > 4 {
		t.Fatalf("concurrency escaped task budget: %d", peak.Load())
	}
	if progress.Load() != int64(len(data)) {
		t.Fatalf("overlap counted twice: %d", progress.Load())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".part-") {
			t.Fatal("new engine created part files")
		}
	}
	r, err := readRangeRecord(path)
	if err != nil || r.received() != int64(len(data)) {
		t.Fatal("completion was not durable", err)
	}
}

func TestRangeEngineResumeWithDifferentConcurrency(t *testing.T) {
	data := engineData(2 * 1024 * 1024)
	for _, threads := range []int{1, 8} {
		t.Run(strconv.Itoa(threads), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "media")
			seedRangeRecord(t, path, int64(len(data)), 4, 32768)
			f, err := os.OpenFile(path, os.O_RDWR, 0644)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 4; i++ {
				start := i * len(data) / 4
				if _, err := f.WriteAt(data[start:start+32768], int64(start)); err != nil {
					t.Fatal(err)
				}
			}
			f.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var start, end int
				_, _ = fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
				if start%(len(data)/4) < 32768 {
					t.Errorf("requested an already saved interval: %d", start)
				}
				http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(data))
			}))
			defer server.Close()
			s := NewStreamDownloader([]string{server.URL}, path)
			s.totalSize = int64(len(data))
			var network int64
			if err := s.DownloadWithConcurrency(context.Background(), threads, func(n int64) { network += n }); err != nil {
				t.Fatal(err)
			}
			assertMediaBytes(t, path, data)
			if network != int64(len(data)-4*32768) {
				t.Fatalf("resume progress %d", network)
			}
		})
	}
}

func TestRangeEngineCorruptRecordCannotCompleteSparseFile(t *testing.T) {
	data := engineData(256 * 1024)
	for _, record := range []string{`{broken`, `{"version":1,"total":262144,"ranges":[{"start":0,"position":262144,"end":999999}]}`} {
		path := filepath.Join(t.TempDir(), "media")
		if err := os.WriteFile(path, make([]byte, len(data)), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rangeRecordPath(path), []byte(record), 0600); err != nil {
			t.Fatal(err)
		}
		if existingStreamSize(path) != 0 {
			t.Fatal("sparse file was counted as received data")
		}
		server := bandwidthServer(t, data, true)
		s := NewStreamDownloader([]string{server.URL}, path)
		s.totalSize = int64(len(data))
		if err := s.DownloadWithConcurrency(context.Background(), 4, nil); err != nil {
			t.Fatal(err)
		}
		assertMediaBytes(t, path, data)
	}
}

func TestRangeEngineChangedETagRestartsSameSizeFile(t *testing.T) {
	data := engineData(256 * 1024)
	path := filepath.Join(t.TempDir(), "media")
	seedRangeRecord(t, path, int64(len(data)), 2, 4096)
	r, err := readRangeRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	r.ETag = `"old"`
	if err := writeRangeRecord(path, r); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"new"`)
		http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(data))
	}))
	defer server.Close()
	s := NewStreamDownloader([]string{server.URL}, path)
	var delta int64
	if err := s.DownloadWithConcurrency(context.Background(), 4, func(n int64) { delta += n }); err != nil {
		t.Fatal(err)
	}
	assertMediaBytes(t, path, data)
	if delta != int64(len(data)-8192) {
		t.Fatalf("reset progress drifted: %d", delta)
	}
}

func TestRangeEngineValidatesCompletedTrackBeforeReuse(t *testing.T) {
	for _, mode := range []string{"unchanged", "changed", "weak", "missing", "offline"} {
		t.Run(mode, func(t *testing.T) {
			old, fresh := []byte("old track"), []byte("new track")
			var resumed atomic.Bool
			var probes, transfers atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, tag := old, `"old"`
				if resumed.Load() {
					if r.Header.Get("Range") == "bytes=0-0" {
						probes.Add(1)
					} else {
						transfers.Add(1)
					}
					switch mode {
					case "changed":
						data, tag = fresh, `"new"`
					case "weak", "missing":
						data = fresh
					case "offline":
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
				}
				switch mode {
				case "weak":
					w.Header().Set("ETag", `W/"old"`)
				case "missing":
				default:
					w.Header().Set("ETag", tag)
				}
				http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(data))
			}))
			defer server.Close()

			path := filepath.Join(t.TempDir(), "media")
			start := func(query string) error {
				urls := []string{server.URL + "/media?" + query}
				s := NewStreamDownloader(urls, path)
				s.representation = mediaRepresentationKey(urls)
				s.skipSizeProbe = true
				return s.DownloadWithConcurrency(context.Background(), 2, nil)
			}
			if err := start("signature=first"); err != nil {
				t.Fatal(err)
			}
			resumed.Store(true)
			err := start("signature=second")
			if probes.Load() != 1 {
				t.Fatalf("expected one verification request, got %d", probes.Load())
			}
			if mode == "offline" {
				if err == nil || transfers.Load() != 0 {
					t.Fatalf("probe failure must preserve the completed checkpoint: %v", err)
				}
				record, recordErr := readRangeRecord(path)
				if recordErr != nil || record.received() != int64(len(old)) {
					t.Fatalf("probe failure changed checkpoint: %v %+v", recordErr, record)
				}
				assertMediaBytes(t, path, old)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unchanged" {
				if transfers.Load() != 0 {
					t.Fatal("matching strong validator should avoid downloading the same bytes")
				}
				assertMediaBytes(t, path, old)
			} else {
				if transfers.Load() == 0 {
					t.Fatal("unverified media was treated as completed")
				}
				assertMediaBytes(t, path, fresh)
			}
		})
	}
}

func TestRangeEngineChangedTrackPathDiscardsPartialBytes(t *testing.T) {
	old, fresh := []byte("old track"), []byte("new track")
	path := filepath.Join(t.TempDir(), "media")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(fresh))
	}))
	defer server.Close()
	writeStorageFixture(t, path, old[:4])
	if err := writeRangeRecord(path, rangeRecord{Version: 1, Total: int64(len(old)),
		Representation: mediaRepresentationKey([]string{server.URL + "/old"}),
		Ranges:         []byteRange{{Position: 4, End: int64(len(old))}}}); err != nil {
		t.Fatal(err)
	}
	urls := []string{server.URL + "/new"}
	s := NewStreamDownloader(urls, path)
	s.representation, s.skipSizeProbe = mediaRepresentationKey(urls), true
	if err := s.DownloadWithConcurrency(context.Background(), 2, nil); err != nil {
		t.Fatal(err)
	}
	assertMediaBytes(t, path, fresh)
}

func TestRangeEngine404TriesRemainingSourcesAndStopsWhenExhausted(t *testing.T) {
	for _, backupAvailable := range []bool{true, false} {
		t.Run(strconv.FormatBool(backupAvailable), func(t *testing.T) {
			data := []byte("media")
			var primary, backup atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/primary" {
					primary.Add(1)
					http.NotFound(w, r)
					return
				}
				backup.Add(1)
				if !backupAvailable {
					http.NotFound(w, r)
					return
				}
				http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(data))
			}))
			defer server.Close()
			s := NewStreamDownloader([]string{server.URL + "/primary", server.URL + "/backup"}, filepath.Join(t.TempDir(), "media"))
			s.skipSizeProbe = true
			err := s.DownloadWithConcurrency(context.Background(), 4, nil)
			if primary.Load() != 1 || backup.Load() != 1 {
				t.Fatalf("incorrect 404 fallback: primary=%d backup=%d error=%v", primary.Load(), backup.Load(), err)
			}
			if backupAvailable {
				if err != nil {
					t.Fatal(err)
				}
				assertMediaBytes(t, s.targetPath, data)
			} else if err == nil || !strings.Contains(err.Error(), "404") {
				t.Fatalf("missing source error: %v", err)
			}
		})
	}
}

func TestRangeEngineMissingRecordCannotCompleteSparseFile(t *testing.T) {
	data := engineData(256 * 1024)
	path := filepath.Join(t.TempDir(), "media")
	if err := os.WriteFile(path, make([]byte, len(data)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ensureRangeMarker(path); err != nil {
		t.Fatal(err)
	}
	if existingStreamSize(path) != 0 {
		t.Fatal("missing manifest counted a sparse file as complete")
	}
	server := bandwidthServer(t, data, true)
	s := NewStreamDownloader([]string{server.URL}, path)
	s.totalSize = int64(len(data))
	if err := s.DownloadWithConcurrency(context.Background(), 4, nil); err != nil {
		t.Fatal(err)
	}
	assertMediaBytes(t, path, data)
}

func TestRangeEngineKeepsHealthyConnectionWhenNewRangesAreRejected(t *testing.T) {
	data := engineData(512 * 1024)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) > 1 {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data)))
		w.WriteHeader(206)
		for offset := 0; offset < len(data); offset += 4096 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Millisecond):
			}
			if _, err := w.Write(data[offset:min(offset+4096, len(data))]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	defer server.Close()
	s := NewStreamDownloader([]string{server.URL}, filepath.Join(t.TempDir(), "media"))
	s.totalSize = int64(len(data))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.DownloadWithConcurrency(ctx, 8, nil); err != nil {
		t.Fatal("healthy connection was killed by additional range failures:", err)
	}
	assertMediaBytes(t, s.targetPath, data)
}

func TestRangeEngineSharesNetworkSlotsAcrossTasks(t *testing.T) {
	const slots = 4
	started := make(chan struct{}, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int64
		_, _ = fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Length", fmt.Sprint(end-start+1))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, 1024*1024))
		w.WriteHeader(206)
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	budget := make(connectionBudget, slots)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results := make(chan error, 3)
	for i := 0; i < 3; i++ {
		s := NewStreamDownloader([]string{server.URL}, filepath.Join(t.TempDir(), "media"))
		s.totalSize = 1024 * 1024
		s.connections = budget
		go func() { results <- s.DownloadWithConcurrency(ctx, 8, nil) }()
	}
	for i := 0; i < slots; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("shared slots did not fill")
		}
	}
	select {
	case <-started:
		t.Error("tasks exceeded the shared connection budget")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	for i := 0; i < 3; i++ {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if len(budget) != 0 {
		t.Fatal("cancellation leaked connection slots")
	}
}

func TestRangeEngineCoalescesExpiredURLsAcrossWorkers(t *testing.T) {
	data := engineData(2 * 1024 * 1024)
	fresh := bandwidthServer(t, data, true)
	var requests, refreshes atomic.Int32
	expired := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data)))
			w.WriteHeader(206)
			_, _ = w.Write(data[:32768])
			return
		}
		w.WriteHeader(403)
	}))
	defer expired.Close()
	path := filepath.Join(t.TempDir(), "media")
	s := NewStreamDownloader([]string{expired.URL}, path)
	s.totalSize = int64(len(data))
	s.SetURLRefresher(func(ctx context.Context) ([]string, error) { refreshes.Add(1); return []string{fresh.URL}, nil })
	if err := s.DownloadWithConcurrency(context.Background(), 8, nil); err != nil {
		t.Fatal(err)
	}
	assertMediaBytes(t, path, data)
	if refreshes.Load() != 1 {
		t.Fatalf("refresh storm: %d calls", refreshes.Load())
	}
}

func TestRangeEngineHonorsRetryAfter(t *testing.T) {
	data := []byte("rate limited media")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(data))
	}))
	defer server.Close()
	s := NewStreamDownloader([]string{server.URL}, filepath.Join(t.TempDir(), "media"))
	s.totalSize = int64(len(data))
	start := time.Now()
	if err := s.DownloadSingleStream(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || time.Since(start) < time.Second {
		t.Fatal("Retry-After was ignored or retried excessively")
	}
}

func TestRangeEngineChangedSignedURLDoesNotResetFailureBudget(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(403) }))
	defer server.Close()
	s := NewStreamDownloader([]string{server.URL}, filepath.Join(t.TempDir(), "media"))
	s.totalSize = 1024
	var refreshes atomic.Int32
	s.SetURLRefresher(func(context.Context) ([]string, error) {
		return []string{fmt.Sprintf("%s?generation=%d", server.URL, refreshes.Add(1))}, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := s.DownloadSingleStream(ctx, nil)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("failure budget was replaced by an endless URL refresh loop", err)
	}
	if calls.Load() != 8 {
		t.Fatalf("continuous failure attempts=%d, want 8", calls.Load())
	}
}

func TestRangeEngineCheckpointFailureStopsBeforeNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media")
	if err := os.Mkdir(rangeRecordPath(path), 0700); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s := NewStreamDownloader([]string{"https://cdn.invalid/media"}, path)
	s.totalSize = 1024
	s.client.Transport = streamRoundTripperFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, io.EOF })
	if err := s.DownloadWithConcurrency(context.Background(), 4, nil); err == nil {
		t.Fatal("checkpoint failure was ignored")
	}
	if calls.Load() != 0 {
		t.Fatal("network continued without durable range state")
	}
}

func TestConnectionBudgetCancellation(t *testing.T) {
	b := make(connectionBudget, 1)
	release, err := b.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPlaybackRefreshRejectsRepresentationChange(t *testing.T) {
	original := &bilibili.StreamSelection{QualityID: 80, Codec: "AVC"}
	refresh := playbackRefresher(original, func(context.Context) (*bilibili.StreamSelection, error) {
		return &bilibili.StreamSelection{QualityID: 16, Codec: "AVC"}, nil
	})
	if _, err := refresh(context.Background()); err == nil {
		t.Fatal("mixed media representations could be resumed")
	}
}

func TestPlaybackRefreshAcceptsNewSignatureButRejectsDifferentAudio(t *testing.T) {
	original := &bilibili.StreamSelection{
		QualityID: 80, Codec: "AVC",
		VideoURLs: []string{"https://cdn.example/video.m4s?token=old"},
		AudioURLs: []string{"https://cdn.example/audio.m4s?token=old"},
	}
	fresh := *original
	fresh.VideoURLs = []string{"https://backup.example/video.m4s?token=new"}
	fresh.AudioURLs = []string{"https://backup.example/audio.m4s?token=new"}
	try := func() error {
		refresh := playbackRefresher(original, func(context.Context) (*bilibili.StreamSelection, error) { return &fresh, nil })
		_, err := refresh(context.Background())
		return err
	}
	if err := try(); err != nil {
		t.Fatal(err)
	}
	fresh.AudioURLs = []string{"https://backup.example/other-audio.m4s?token=new"}
	if err := try(); err == nil {
		t.Fatal("audio track changed without changing the video quality or codec")
	}
}

func TestPlaybackRefreshCoalescesConcurrentTracks(t *testing.T) {
	original := &bilibili.StreamSelection{QualityID: 80, Codec: "AVC"}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	refresh := playbackRefresher(original, func(ctx context.Context) (*bilibili.StreamSelection, error) {
		calls.Add(1)
		close(started)
		<-release
		return original, nil
	})
	results := make(chan error, 2)
	go func() { _, err := refresh(context.Background()); results <- err }()
	<-started
	go func() { _, err := refresh(context.Background()); results <- err }()
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("video/audio refresh duplicated the playurl request")
	}
}

func TestRangeRecordMigrationAndCleanup(t *testing.T) {
	old, next := filepath.Join(t.TempDir(), "old-media"), filepath.Join(t.TempDir(), "new-media")
	seedRangeRecord(t, old, 1024*1024, 4, 4096)
	if err := migrateStream(old, next); err != nil {
		t.Fatal(err)
	}
	if got := existingStreamSize(next); got != 4*4096 {
		t.Fatalf("migration lost sparse progress: %d", got)
	}
	if _, err := os.Stat(rangeMarkerPath(next)); err != nil {
		t.Fatal("migration dropped sparse marker", err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("migration deleted source before task paths were saved", err)
	}
	cleanupStreamArtifacts(next)
	for _, path := range []string{next, rangeRecordPath(next), rangeMarkerPath(next)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("cleanup left an artifact", path, err)
		}
	}
}
