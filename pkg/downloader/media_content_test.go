package downloader

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func mockJSONLabeledMedia(r *http.Request, data []byte, etag string) *http.Response {
	w := httptest.NewRecorder()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("ETag", etag)
	http.ServeContent(w, r, "audio.m4s", time.Time{}, bytes.NewReader(data))
	resp := w.Result()
	resp.Request = r
	return resp
}

func TestMislabeledMP4TransfersAndReusesCompletedTrack(t *testing.T) {
	data := engineData(3 * 1024 * 1024)
	copy(data, "\x00\x00\x00\x24ftypM4A \x00\x00\x00\x00")
	urls := []string{"https://primary.invalid/audio.m4s"}
	path := filepath.Join(t.TempDir(), "audio")

	var probes, transfers atomic.Int32
	download := func() error {
		s := NewStreamDownloader(urls, path)
		s.representation = mediaRepresentationKey(urls)
		s.sizeHint, s.skipSizeProbe = int64(len(data)), true
		s.client = &http.Client{Transport: streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			switch r.Header.Get("Range") {
			case "bytes=0-0", "bytes=0-15":
				probes.Add(1)
			default:
				transfers.Add(1)
			}
			return mockJSONLabeledMedia(r, data, `"audio"`), nil
		})}
		return s.DownloadWithConcurrency(context.Background(), 4, nil)
	}
	if err := download(); err != nil {
		t.Fatal(err)
	}
	assertMediaBytes(t, path, data)
	firstTransfers := transfers.Load()
	if firstTransfers < 2 || probes.Load() != 0 {
		t.Fatalf("expected parallel transfer without a prefix request: transfers=%d probes=%d", firstTransfers, probes.Load())
	}

	if err := download(); err != nil {
		t.Fatal(err)
	}
	if transfers.Load() != firstTransfers || probes.Load() != 2 {
		t.Fatalf("expected to reuse the completed track after two tiny probes: transfers=%d probes=%d", transfers.Load(), probes.Load())
	}
}

func TestMediaTracksDownloadMislabeledVideo(t *testing.T) {
	videoData := engineData(256 * 1024)
	copy(videoData, "\x00\x00\x00\x24ftypisom\x00\x00\x00\x00")
	audioData := engineData(32 * 1024)
	dir := t.TempDir()
	video := NewStreamDownloader([]string{"https://cdn.invalid/video.m4s"}, filepath.Join(dir, "video.m4s"))
	audio := NewStreamDownloader([]string{"https://cdn.invalid/audio.m4s"}, filepath.Join(dir, "audio.m4s"))
	video.skipSizeProbe, audio.skipSizeProbe = true, true
	video.sizeHint, audio.sizeHint = int64(len(videoData)), int64(len(audioData))
	video.client = &http.Client{Transport: streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return mockJSONLabeledMedia(r, videoData, `"video"`), nil
	})}
	audio.client = &http.Client{Transport: streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return mockRangeMedia(r, audioData), nil
	})}

	if failure := downloadMediaTracks(context.Background(), video, audio, 4, nil); failure != nil {
		t.Fatal(failure)
	}
	assertMediaBytes(t, video.targetPath, videoData)
	assertMediaBytes(t, audio.targetPath, audioData)
}

func TestMislabeledMP4ResumesOnVerifiedBackup(t *testing.T) {
	data := engineData(256 * 1024)
	copy(data, "\x00\x00\x00\x24ftypM4A \x00\x00\x00\x00")
	const saved = 64 * 1024
	const primary = "https://primary.invalid/audio.m4s"
	const backup = "https://backup.invalid/audio.m4s"
	urls := []string{primary, backup}
	path := filepath.Join(t.TempDir(), "audio")
	s := NewStreamDownloader(urls, path)
	s.representation, s.skipSizeProbe = mediaRepresentationKey(urls), true
	writeStorageFixture(t, path, data[:saved])
	if err := writeRangeRecord(path, rangeRecord{
		Version: 1, Total: int64(len(data)), Representation: s.representation,
		ETag: `"primary"`, ValidatorKey: mediaValidatorKey(primary),
		Ranges: []byteRange{{End: int64(len(data)), Position: saved}},
	}); err != nil {
		t.Fatal(err)
	}

	var probes atomic.Int32
	s.client = &http.Client{Transport: streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "primary.invalid" {
			resp := mockRangeMedia(r, data)
			resp.StatusCode = http.StatusNotFound
			return resp, nil
		}
		if r.Header.Get("If-Range") != "" {
			t.Error("sent the primary validator to the backup")
		}
		if r.Header.Get("Range") == "bytes=0-15" {
			probes.Add(1)
		} else if !strings.HasPrefix(r.Header.Get("Range"), "bytes=65536-") {
			t.Errorf("unexpected transfer range %q", r.Header.Get("Range"))
		}
		return mockJSONLabeledMedia(r, data, `"backup"`), nil
	})}
	var downloaded int64
	if err := s.DownloadWithConcurrency(context.Background(), 1, func(n int64) { downloaded += n }); err != nil {
		t.Fatal(err)
	}
	assertMediaBytes(t, path, data)
	if downloaded != int64(len(data))-saved || probes.Load() != 1 {
		t.Fatalf("resume downloaded %d bytes, used %d prefix probes", downloaded, probes.Load())
	}
}

func TestMislabeledMP4RejectsActualJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio")
	s := NewStreamDownloader([]string{"https://primary.invalid/audio.m4s"}, path)
	s.skipSizeProbe = true
	s.sizeHint = 32
	s.client = &http.Client{Transport: streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return mockJSONLabeledMedia(r, []byte(`{"code":-1,"message":"unavailable"}`), `"error-document"`), nil
	})}
	if err := s.DownloadWithConcurrency(context.Background(), 4, nil); !errors.Is(err, errNonMediaResponse) {
		t.Fatalf("expected non-media response, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("JSON response left a file: %v", err)
	}
}
