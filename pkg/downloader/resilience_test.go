package downloader

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestResilience_FirstByteNonMediaSwitchesToBackupCDN(t *testing.T) {
	data := engineData(512 * 1024)
	backup := bandwidthServer(t, data, true)

	var primaryHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", "18")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "<html>blocked</html>")
	}))
	defer primary.Close()

	path := filepath.Join(t.TempDir(), "media")
	dl := NewStreamDownloader([]string{primary.URL, backup.URL}, path)
	dl.skipSizeProbe = true

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := dl.DownloadWithConcurrency(ctx, 4, nil); err != nil {
		t.Fatalf("first-byte HTML response failed to switch to backup CDN: %v", err)
	}
	assertMediaBytes(t, path, data)
	if primaryHits.Load() != 1 {
		t.Fatalf("expected primary to be rejected after 1 hit, got %d", primaryHits.Load())
	}
}

func TestResilience_FirstByteIgnoredRangeSwitchesToBackupCDN(t *testing.T) {
	data := engineData(512 * 1024)

	var primaryHits, backupHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer primary.Close()

	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupHits.Add(1)
		http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(data))
	}))
	defer backup.Close()

	path := filepath.Join(t.TempDir(), "media")
	dl := NewStreamDownloader([]string{primary.URL, backup.URL}, path)
	dl.skipSizeProbe = true

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := dl.DownloadWithConcurrency(ctx, 4, nil); err != nil {
		t.Fatalf("ignored Range on first byte failed to switch to backup CDN: %v", err)
	}
	assertMediaBytes(t, path, data)
	if primaryHits.Load() != 1 || backupHits.Load() == 0 {
		t.Fatalf("unexpected failover pattern: primary=%d backup=%d", primaryHits.Load(), backupHits.Load())
	}
}

func TestResilience_HTTP400And451FallbackToBackupAndRefresh(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusUnavailableForLegalReasons} {
		t.Run(fmt.Sprintf("backup-%d", code), func(t *testing.T) {
			data := engineData(256 * 1024)
			backup := bandwidthServer(t, data, true)

			var primaryHits atomic.Int32
			primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				primaryHits.Add(1)
				w.WriteHeader(code)
			}))
			defer primary.Close()

			path := filepath.Join(t.TempDir(), "media")
			dl := NewStreamDownloader([]string{primary.URL, backup.URL}, path)
			dl.skipSizeProbe = true

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := dl.DownloadWithConcurrency(ctx, 4, nil); err != nil {
				t.Fatalf("HTTP %d failed to failover to backup CDN: %v", code, err)
			}
			assertMediaBytes(t, path, data)
			if primaryHits.Load() != 1 {
				t.Fatalf("expected rejected primary to be hit once, got %d", primaryHits.Load())
			}
		})
	}

	t.Run("all-rejected-refreshes", func(t *testing.T) {
		data := engineData(256 * 1024)
		fresh := bandwidthServer(t, data, true)

		var primaryHits, backupHits, refreshes atomic.Int32
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			primaryHits.Add(1)
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer primary.Close()
		backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backupHits.Add(1)
			w.WriteHeader(http.StatusUnavailableForLegalReasons)
		}))
		defer backup.Close()

		path := filepath.Join(t.TempDir(), "media")
		dl := NewStreamDownloader([]string{primary.URL, backup.URL}, path)
		dl.skipSizeProbe = true
		dl.SetURLRefresher(func(context.Context) ([]string, error) {
			refreshes.Add(1)
			return []string{fresh.URL}, nil
		})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := dl.DownloadWithConcurrency(ctx, 4, nil); err != nil {
			t.Fatalf("refresh after all sources rejected failed: %v", err)
		}
		assertMediaBytes(t, path, data)
		if refreshes.Load() != 1 || primaryHits.Load() != 1 || backupHits.Load() != 1 {
			t.Fatalf("unexpected counts: refreshes=%d primary=%d backup=%d", refreshes.Load(), primaryHits.Load(), backupHits.Load())
		}
	})

	t.Run("single-400-no-refresher-fails-fast", func(t *testing.T) {
		var hits atomic.Int32
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusBadRequest)
		}))
		defer primary.Close()

		path := filepath.Join(t.TempDir(), "media")
		dl := NewStreamDownloader([]string{primary.URL}, path)
		dl.skipSizeProbe = true

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := dl.DownloadWithConcurrency(ctx, 2, nil)
		if err == nil || !strings.Contains(err.Error(), "400") {
			t.Fatalf("expected HTTP 400 error, got %v", err)
		}
		if hits.Load() != 1 {
			t.Fatalf("expected single 400 without refresher to fail fast, hits=%d", hits.Load())
		}
	})
}

func TestResilience_AllSources404RefreshesURLs(t *testing.T) {
	data := engineData(256 * 1024)
	fresh := bandwidthServer(t, data, true)

	var primaryHits, backupHits, refreshes atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits.Add(1)
		http.NotFound(w, r)
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupHits.Add(1)
		http.NotFound(w, r)
	}))
	defer backup.Close()

	path := filepath.Join(t.TempDir(), "media")
	dl := NewStreamDownloader([]string{primary.URL, backup.URL}, path)
	dl.skipSizeProbe = true
	dl.SetURLRefresher(func(context.Context) ([]string, error) {
		refreshes.Add(1)
		return []string{fresh.URL}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := dl.DownloadWithConcurrency(ctx, 4, nil); err != nil {
		t.Fatalf("404 across all sources did not recover via refresh: %v", err)
	}
	assertMediaBytes(t, path, data)
	if refreshes.Load() != 1 || primaryHits.Load() != 1 || backupHits.Load() != 1 {
		t.Fatalf("unexpected counts: refreshes=%d primary=%d backup=%d", refreshes.Load(), primaryHits.Load(), backupHits.Load())
	}
}

func TestResilience_Startup416WithOversizedHintRecovers(t *testing.T) {
	for _, withContentRange := range []bool{true, false} {
		name := "without-content-range"
		if withContentRange {
			name = "with-content-range"
		}
		t.Run(name, func(t *testing.T) {
			data := engineData(256 * 1024)
			var rejections atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if rangeHdr := r.Header.Get("Range"); strings.HasPrefix(rangeHdr, "bytes=") {
					parts := strings.Split(strings.TrimPrefix(rangeHdr, "bytes="), "-")
					if len(parts) == 2 && parts[1] != "" {
						end, err := strconv.Atoi(parts[1])
						if err == nil && end >= len(data) {
							rejections.Add(1)
							if withContentRange {
								w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(data)))
							}
							w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
							return
						}
					}
				}
				http.ServeContent(w, r, "media", time.Time{}, bytes.NewReader(data))
			}))
			defer server.Close()

			path := filepath.Join(t.TempDir(), "media")
			dl := NewStreamDownloader([]string{server.URL}, path)
			dl.skipSizeProbe = true
			dl.sizeHint = 10 * 1024 * 1024

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := dl.DownloadWithConcurrency(ctx, 4, nil); err != nil {
				t.Fatalf("startup 416 with oversized hint failed: %v", err)
			}
			assertMediaBytes(t, path, data)
			if rejections.Load() != 1 {
				t.Fatalf("expected 1 startup 416 rejection before recovery, got %d", rejections.Load())
			}
		})
	}
}

func TestResilience_RepeatedStartup416UsesBackup(t *testing.T) {
	const primary = "https://primary.invalid/media"
	const backup = "https://backup.invalid/media"
	data := engineData(32 * 1024)
	path := filepath.Join(t.TempDir(), "media")
	s := NewStreamDownloader([]string{primary, backup}, path)
	s.skipSizeProbe = true
	s.sizeHint = int64(2 * len(data))

	var hits atomic.Int32
	s.client.Transport = streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == primary {
			hits.Add(1)
			return mockRangeRejection(r, len(data)), nil
		}
		return mockRangeMedia(r, data), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.DownloadWithConcurrency(ctx, 4, nil); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 2 {
		t.Fatalf("expected one corrected retry before failover, got %d primary requests", got)
	}
	assertMediaBytes(t, path, data)
}

func TestResilience_Concurrent416PreservesVerifiedRanges(t *testing.T) {
	const primary = "https://primary.invalid/media"
	const backup = "https://backup.invalid/media"
	data := engineData(64 * 1024)
	total := int64(len(data))
	const prefix = 4096
	path := filepath.Join(t.TempDir(), "media")
	ranges := []byteRange{
		{Start: 0, Position: prefix, End: total / 2},
		{Start: total / 2, Position: total/2 + prefix, End: total},
	}
	partial := make([]byte, len(data))
	for _, r := range ranges {
		copy(partial[r.Start:r.Position], data[r.Start:r.Position])
	}
	if err := os.WriteFile(path, partial, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeRangeRecord(path, rangeRecord{Version: 1, Total: total, Ranges: ranges}); err != nil {
		t.Fatal(err)
	}

	s := NewStreamDownloader([]string{primary, backup}, path)
	both := make(chan struct{})
	var rejected atomic.Int32
	s.client.Transport = streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != primary {
			return mockRangeMedia(r, data), nil
		}
		if rejected.Add(1) == 2 {
			close(both)
		}
		select {
		case <-both:
			return mockRangeRejection(r, len(data)/4), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})

	var received int64
	e, err := s.prepareTransfer(newSourcePool(s.urls, nil), 2, false, func(n int64) { received += n })
	if err != nil {
		t.Fatal(err)
	}
	defer e.file.Close()
	e.confirmed, e.canRange = true, true
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.run(ctx); err != nil {
		t.Fatal(err)
	}
	if s.totalSize != total || received != total-2*prefix {
		t.Fatalf("expected only missing bytes and unchanged size, received=%d size=%d", received, s.totalSize)
	}
	assertMediaBytes(t, path, data)
}

func TestResilience_416RestartRequiresVerifiedSizeChange(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("size-changed-%t", changed), func(t *testing.T) {
			t.Parallel()
			const originalSize = 64 * 1024
			const prefix = 48 * 1024
			data := engineData(originalSize)
			if changed {
				data = data[:originalSize/2]
			}
			path := filepath.Join(t.TempDir(), "media")
			seedRangeRecord(t, path, originalSize, 1, prefix)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			s := NewStreamDownloader([]string{"https://cdn.invalid/media"}, path)
			s.skipSizeProbe = true
			var probes atomic.Int32
			s.client.Transport = streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				switch r.Header.Get("Range") {
				case "bytes=0-0":
					probes.Add(1)
					return mockRangeMedia(r, data), nil
				case "bytes=49152-65535":
					return mockRangeRejection(r, originalSize/2), nil
				default:
					return mockRangeMedia(r, data), nil
				}
			})

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			err = s.DownloadWithConcurrency(ctx, 2, nil)
			wantProbes := int32(1)
			if changed {
				wantProbes = 2
			}
			if probes.Load() != wantProbes {
				t.Fatalf("got %d verification probes, want %d: %v", probes.Load(), wantProbes, err)
			}
			if changed {
				if err != nil {
					t.Fatal(err)
				}
				assertMediaBytes(t, path, data)
				return
			}

			var status *mediaHTTPError
			if !errors.As(err, &status) || status.code != http.StatusRequestedRangeNotSatisfiable {
				t.Fatalf("expected range error with original checkpoint retained, got %v", err)
			}
			record, err := readRangeRecord(path)
			if err != nil || record.Total != originalSize || record.received() != prefix {
				t.Fatalf("original checkpoint was modified: %+v, %v", record, err)
			}
			assertMediaBytes(t, path, before)
		})
	}
}

func TestResilience_EntityConflictUsesBackupWithoutResetting(t *testing.T) {
	const primary, backup = "https://primary.invalid/media", "https://backup.invalid/media"
	const size, prefix = 512 * 1024, 4096
	data := engineData(size)
	for _, mode := range []string{"size", "last-modified"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "media")
			seedRangeRecord(t, path, size, 1, prefix)
			record, err := readRangeRecord(path)
			if err != nil {
				t.Fatal(err)
			}
			record.ValidatorKey = mediaValidatorKey(primary)
			if mode == "last-modified" {
				record.LastModified = "Sun, 22 Nov 2026 01:29:02 GMT"
			}
			if err := writeRangeRecord(path, record); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(bytes.Clone(data[:prefix]), make([]byte, size-prefix)...), 0600); err != nil {
				t.Fatal(err)
			}
			s := NewStreamDownloader([]string{primary, backup}, path)
			s.skipSizeProbe = true
			var backupRequests atomic.Int32
			s.client.Transport = streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Range") == "" || strings.HasPrefix(r.Header.Get("Range"), "bytes=0-") {
					t.Errorf("valid prefix was abandoned: %q", r.Header.Get("Range"))
				}
				if r.URL.String() == backup {
					backupRequests.Add(1)
					return mockRangeMedia(r, data), nil
				}
				if mode == "size" {
					return mockRangeMedia(r, data[:size/2]), nil
				}
				resp := mockRangeMedia(r, data)
				resp.Header.Set("Last-Modified", "Mon, 23 Nov 2026 01:29:02 GMT")
				return resp, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var delta int64
			if err := s.DownloadWithConcurrency(ctx, 4, func(n int64) { delta += n }); err != nil {
				t.Fatal(err)
			}
			if delta != size-prefix || backupRequests.Load() == 0 {
				t.Fatalf("want only missing bytes from backup, got delta=%d backup requests=%d", delta, backupRequests.Load())
			}
			assertMediaBytes(t, path, data)
		})
	}
}

func TestResilience_SingleSourceEntityConflictRequiresConsistentEvidence(t *testing.T) {
	const size, prefix = 64 * 1024, 4096
	data := engineData(size)
	for _, recover := range []bool{true, false} {
		t.Run(fmt.Sprintf("recover-%t", recover), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "media")
			seedRangeRecord(t, path, size, 1, prefix)
			partial := append(bytes.Clone(data[:prefix]), make([]byte, size-prefix)...)
			if err := os.WriteFile(path, partial, 0600); err != nil {
				t.Fatal(err)
			}
			s := NewStreamDownloader([]string{"https://cdn.invalid/media"}, path)
			s.skipSizeProbe = true
			var probes, requests atomic.Int32
			s.client.Transport = streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Range") == "bytes=0-0" {
					if probes.Add(1) == 2 {
						if recover {
							return mockRangeMedia(r, data), nil
						}
						return mockRangeMedia(r, data[:size/4]), nil
					}
					return mockRangeMedia(r, data[:size/2]), nil
				}
				if !strings.HasPrefix(r.Header.Get("Range"), "bytes=4096-") {
					t.Errorf("expected the saved offset, got %q", r.Header.Get("Range"))
				}
				if requests.Add(1) == 1 {
					return mockRangeMedia(r, data[:size/2]), nil
				}
				return mockRangeMedia(r, data), nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := s.DownloadWithConcurrency(ctx, 4, nil)
			if probes.Load() != 2 {
				t.Fatalf("want two probes, got %d", probes.Load())
			}
			if recover {
				if err != nil {
					t.Fatal(err)
				}
				assertMediaBytes(t, path, data)
				return
			}
			if !errors.Is(err, errEntityChanged) || !strings.Contains(err.Error(), "响应总长度") {
				t.Fatalf("expected a diagnostic entity conflict, got %v", err)
			}
			record, err := readRangeRecord(path)
			if err != nil || record.Total != size || record.received() != prefix {
				t.Fatalf("original checkpoint was modified: %+v, %v", record, err)
			}
			assertMediaBytes(t, path, partial)
		})
	}
}

func mockRangeRejection(r *http.Request, reportedTotal int) *http.Response {
	w := httptest.NewRecorder()
	w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", reportedTotal))
	w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	resp := w.Result()
	resp.Request = r
	return resp
}

func mockRangeMedia(r *http.Request, data []byte) *http.Response {
	w := httptest.NewRecorder()
	http.ServeContent(w, r, "media.mp4", time.Time{}, bytes.NewReader(data))
	resp := w.Result()
	resp.Request = r
	return resp
}

func TestResilience_ExhaustedPoolWaitsForSharedRefresh(t *testing.T) {
	for _, mark := range []string{"missing", "rejected"} {
		t.Run(mark, func(t *testing.T) {
			const old = "https://old.invalid/media"
			const fresh = "https://fresh.invalid/media"
			started, release := make(chan struct{}), make(chan struct{})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var refreshes atomic.Int32
			p := newSourcePool([]string{old}, func(ctx context.Context) ([]string, error) {
				if refreshes.Add(1) == 1 {
					close(started)
				}
				select {
				case <-release:
					return []string{fresh}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			if mark == "missing" {
				p.notFound(old)
			} else {
				p.reject(old, errNonMediaResponse)
			}
			result := make(chan error, 1)
			go func() {
				got, err := p.next(ctx)
				if err == nil && got != fresh {
					err = fmt.Errorf("expected fresh URL, got %s", got)
				}
				result <- err
			}()
			select {
			case <-started:
			case err := <-result:
				t.Fatalf("first worker exited before refreshing: %v", err)
			case <-ctx.Done():
				t.Fatal("refresh did not start")
			}

			waiterCtx, stop := context.WithTimeout(ctx, 40*time.Millisecond)
			defer stop()
			if _, err := p.next(waiterCtx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("second worker exited while refresh was still pending: %v", err)
			}
			close(release)
			if got, err := p.next(ctx); err != nil || got != fresh {
				t.Fatalf("second worker did not receive fresh URL: %q, %v", got, err)
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			if got := refreshes.Load(); got != 1 {
				t.Fatalf("expected one shared refresh, got %d", got)
			}
		})
	}
}

func TestResilience_ProbeTotalSizeRefreshes(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			testResilienceProbeRefresh(t, status)
		})
	}
}

func testResilienceProbeRefresh(t *testing.T, status int) {
	data := []byte("cached-verified-media-track")
	var expiredHits, freshProbes, freshTransfers, refreshes atomic.Int32

	transport := streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Query().Get("sig"), "old") {
			expiredHits.Add(1)
			w := httptest.NewRecorder()
			w.WriteHeader(status)
			return w.Result(), nil
		}
		if r.Header.Get("Range") == "bytes=0-0" {
			freshProbes.Add(1)
		} else {
			freshTransfers.Add(1)
		}
		resp := mockRangeMedia(r, data)
		resp.Header.Set("ETag", `"v1"`)
		return resp, nil
	})

	path := filepath.Join(t.TempDir(), "media")
	const firstURL = "https://fresh.invalid/track?sig=1"
	dl1 := NewStreamDownloader([]string{firstURL}, path)
	dl1.client.Transport = transport
	dl1.representation = mediaRepresentationKey([]string{firstURL})
	dl1.skipSizeProbe = true
	if err := dl1.DownloadWithConcurrency(context.Background(), 2, nil); err != nil {
		t.Fatalf("initial download failed: %v", err)
	}
	freshProbes.Store(0)
	freshTransfers.Store(0)

	expiredURLs := []string{"https://old.invalid/track?sig=old1", "https://backup.invalid/track?sig=old2"}
	const refreshedURL = "https://fresh.invalid/track?sig=2"
	dl2 := NewStreamDownloader(expiredURLs, path)
	dl2.client.Transport = transport
	dl2.representation = mediaRepresentationKey(expiredURLs)
	dl2.skipSizeProbe = true
	dl2.SetURLRefresher(func(context.Context) ([]string, error) {
		refreshes.Add(1)
		return []string{refreshedURL}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := dl2.DownloadWithConcurrency(ctx, 2, nil); err != nil {
		t.Fatalf("resume verification after refresh failed: %v", err)
	}
	assertMediaBytes(t, path, data)
	if expiredHits.Load() != int32(len(expiredURLs)) || refreshes.Load() != 1 || freshProbes.Load() != 1 || freshTransfers.Load() != 0 {
		t.Fatalf("unexpected verify counts: expired=%d refreshes=%d freshProbes=%d freshTransfers=%d",
			expiredHits.Load(), refreshes.Load(), freshProbes.Load(), freshTransfers.Load())
	}
}

func TestResilience_SlowStreamDemotesSlowPrimary(t *testing.T) {
	t.Run("with-healthy-alternative", func(t *testing.T) {
		p := newSourcePool([]string{"https://primary.invalid/a", "https://backup.invalid/b"}, nil)
		before := time.Now()
		p.demote("https://primary.invalid/a", 30*time.Second)

		p.mu.Lock()
		cooldown := p.sources[0].demotedUntil.Sub(before)
		p.mu.Unlock()
		if cooldown < 29*time.Second {
			t.Fatalf("expected ~30s cooldown when healthy backup exists, got %v", cooldown)
		}

		// Requests started before the demotion can finish in either order.
		p.failed("https://primary.invalid/a", time.Millisecond)
		p.succeeded("https://primary.invalid/a")
		p.mu.Lock()
		cooldownAfterLateResponses := p.sources[0].preferredAfter().Sub(before)
		p.mu.Unlock()
		if cooldownAfterLateResponses < cooldown {
			t.Fatalf("late responses shortened cooldown to %v", cooldownAfterLateResponses)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		got, err := p.next(ctx)
		if err != nil || got != "https://backup.invalid/b" {
			t.Fatalf("expected immediate switch to backup, got %q err=%v", got, err)
		}
		p.notFound("https://backup.invalid/b")
		got, err = p.next(ctx)
		if err != nil || got != "https://primary.invalid/a" {
			t.Fatalf("expected a slow but usable primary when the backup is gone, got %q err=%v", got, err)
		}
	})

	t.Run("single-source-short-backoff", func(t *testing.T) {
		p := newSourcePool([]string{"https://primary.invalid/a"}, nil)
		before := time.Now()
		p.demote("https://primary.invalid/a", 30*time.Second)

		p.mu.Lock()
		cooldown := p.sources[0].ready.Sub(before)
		p.mu.Unlock()
		if cooldown <= 0 || cooldown > time.Second {
			t.Fatalf("expected short single-source backoff instead of 30s, got %v", cooldown)
		}
	})
}

func TestResilience_MediaRouteTransportExpandedHostsAndErrors(t *testing.T) {
	for _, tc := range []struct {
		host string
		want bool
	}{
		{host: "upos-sz-mirrorakam.akamaized.net", want: true},
		{host: "upos-hz-mirrorakam.akamaized.net", want: true},
		{host: "edge.szbdyd.com", want: true},
		{host: "cn-zj-fx-01-02.bilivideo.com", want: true},
		{host: "xy123.bilivideo.cn", want: true},
		{host: "szbdyd.com.example.com", want: false},
		{host: "evilakamaized.net", want: false},
		{host: "example.com", want: false},
	} {
		if got := isBilibiliMediaHost(tc.host); got != tc.want {
			t.Fatalf("isBilibiliMediaHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}

	recoverableErrs := []struct {
		name string
		err  error
		host string
	}{
		{name: "dns", err: &net.DNSError{Err: "no such host", Name: "upos-sz-mirrorakam.akamaized.net"}, host: "upos-sz-mirrorakam.akamaized.net"},
		{name: "econnaborted", err: &net.OpError{Op: "read", Err: os.NewSyscallError("read", syscall.ECONNABORTED)}, host: "edge.szbdyd.com"},
		{name: "ehostunreach", err: &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}, host: "upos-sz-mirrorakam.akamaized.net"},
		{name: "enetunreach", err: &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}, host: "edge.szbdyd.com"},
	}
	for _, tc := range recoverableErrs {
		t.Run("recover-"+tc.name, func(t *testing.T) {
			var directCalls atomic.Int32
			tr := routeFixture(
				streamRoundTripperFunc(func(*http.Request) (*http.Response, error) {
					return nil, tc.err
				}),
				streamRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
					directCalls.Add(1)
					return routeResponse(req), nil
				}),
			)
			req, _ := http.NewRequest(http.MethodGet, "https://"+tc.host+"/media", nil)
			resp, err := tr.RoundTrip(req)
			if err != nil {
				t.Fatalf("expected direct route recovery for %s, got %v", tc.name, err)
			}
			_ = resp.Body.Close()
			if directCalls.Load() != 1 {
				t.Fatalf("expected direct fallback once, got %d", directCalls.Load())
			}
		})
	}

	t.Run("cert-error-never-bypasses-proxy", func(t *testing.T) {
		certErr := x509.UnknownAuthorityError{}
		tr := routeFixture(
			streamRoundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, certErr
			}),
			streamRoundTripperFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("certificate error must not bypass proxy")
				return nil, nil
			}),
		)
		req, _ := http.NewRequest(http.MethodGet, "https://edge.szbdyd.com/media", nil)
		resp, err := tr.RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err != certErr {
			t.Fatalf("expected original cert error, got %v", err)
		}
	})
}

func TestResilience_FriendlyErrorsClearAndUserFacing(t *testing.T) {
	forbiddenTerms := []string{"429", "404", "400", "401", "403", "405", "410", "451", "500", "502", "503", "504", "http", "cdn", "eof", "timeout", "x509", "tls", "dns"}

	cases := []struct {
		name         string
		err          error
		wantExact    string
		wantContains string
	}{
		{name: "nil", err: nil, wantExact: ""},
		{name: "disk-full-syscall", err: &os.PathError{Op: "write", Path: "/tmp/a.mp4", Err: syscall.ENOSPC}, wantExact: diskSpaceMessage},
		{name: "disk-full-text", err: errors.New("write: no space left on device"), wantExact: diskSpaceMessage},
		{name: "permission-denied", err: &os.PathError{Op: "open", Path: "/tmp/a.mp4", Err: os.ErrPermission}, wantContains: "写入权限"},
		{name: "disk-io-error", err: &os.PathError{Op: "write", Path: "/tmp/a.mp4", Err: syscall.EIO}, wantContains: "无法写入下载文件"},
		{name: "dns-error", err: &net.DNSError{Err: "no such host", Name: "cdn.bilivideo.com"}, wantContains: "无法连接网络"},
		{name: "proxy-refused", err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, wantContains: "无法连接网络"},
		{name: "tls-error", err: errors.New("tls: handshake failure"), wantContains: "无法连接网络"},
		{name: "idle-stream", err: fmt.Errorf("下载连续重试耗尽: %w", errIdleStream), wantContains: "超时"},
		{name: "slow-stream", err: fmt.Errorf("下载连续重试耗尽: %w", errSlowStream), wantContains: "超时"},
		{name: "deadline-exceeded", err: context.DeadlineExceeded, wantContains: "超时"},
		{name: "eof", err: fmt.Errorf("下载连续重试耗尽: %w", io.EOF), wantContains: "连接中断"},
		{name: "unexpected-eof", err: io.ErrUnexpectedEOF, wantContains: "连接中断"},
		{name: "conn-reset", err: syscall.ECONNRESET, wantContains: "连接中断"},
		{name: "broken-pipe", err: syscall.EPIPE, wantContains: "连接中断"},
		{name: "conn-aborted", err: syscall.ECONNABORTED, wantContains: "连接中断"},
		{name: "http-403", err: &mediaHTTPError{code: 403}, wantContains: "过期"},
		{name: "http-401", err: &mediaHTTPError{code: 401}, wantContains: "过期"},
		{name: "http-410", err: &mediaHTTPError{code: 410}, wantContains: "过期"},
		{name: "http-429", err: fmt.Errorf("下载连续重试耗尽: %w", &mediaHTTPError{code: 429}), wantExact: "视频服务器暂时繁忙，请稍后点击重试"},
		{name: "http-404", err: &mediaHTTPError{code: 404}, wantExact: "视频服务器暂时繁忙，请稍后点击重试"},
		{name: "http-400", err: &mediaHTTPError{code: 400}, wantExact: "视频服务器暂时繁忙，请稍后点击重试"},
		{name: "http-416", err: &mediaHTTPError{code: 416}, wantExact: "视频服务器暂时繁忙，请稍后点击重试"},
		{name: "http-451", err: &mediaHTTPError{code: 451}, wantExact: "视频服务器暂时繁忙，请稍后点击重试"},
		{name: "http-503", err: fmt.Errorf("下载连续重试耗尽: %w", &mediaHTTPError{code: 503}), wantExact: "视频服务器暂时繁忙，请稍后点击重试"},
		{name: "status-in-url", err: &url.Error{Op: "Get", URL: "https://cdn.invalid/403?token=503&tag=timeout", Err: errors.New("transport failure")}, wantExact: "下载意外中断，请检查网络后点击重试"},
		{name: "digits-in-untyped-error", err: errors.New("Get https://cdn.invalid/403: transport failure"), wantExact: "下载意外中断，请检查网络后点击重试"},
		{name: "non-media-html", err: errors.New("媒体响应类型异常: text/html"), wantExact: "视频服务器暂时繁忙，请稍后点击重试"},
		{name: "track-changed", err: errors.New("刷新后的媒体轨道发生变化，请重试任务"), wantExact: "视频服务器暂时繁忙，请稍后点击重试"},
		{name: "unknown-fallback", err: errors.New("something unexpected happened"), wantExact: "下载意外中断，请检查网络后点击重试"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatFriendlyError(tc.err)
			if tc.wantExact != "" && got != tc.wantExact {
				t.Fatalf("formatFriendlyError(%v) = %q, want %q", tc.err, got, tc.wantExact)
			}
			if tc.err == nil && got != "" {
				t.Fatalf("formatFriendlyError(nil) = %q, want empty", got)
			}
			if tc.wantContains != "" && !strings.Contains(got, tc.wantContains) {
				t.Fatalf("formatFriendlyError(%v) = %q, want substring %q", tc.err, got, tc.wantContains)
			}
			lower := strings.ToLower(got)
			for _, term := range forbiddenTerms {
				if strings.Contains(lower, term) {
					t.Fatalf("user-facing error message %q leaks technical term %q", got, term)
				}
			}
		})
	}
}
