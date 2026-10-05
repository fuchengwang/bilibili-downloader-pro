package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func resumeRelease(url string, data []byte) Release {
	h := sha256.Sum256(data)
	return Release{Version: "1.0.1", Artifact: Artifact{OS: "darwin", Arch: "arm64", FileName: "tool.zip", Size: int64(len(data)), SHA256: hex.EncodeToString(h[:]), Sources: []Source{{Name: "main", URL: url}}}}
}

func resumeClient(t *testing.T, server string) *Client {
	t.Helper()
	c, err := New(Config{ServerURL: server, AppID: "resume-test", CurrentVersion: "1.0.0", OS: "darwin", Arch: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestResumeAcrossClientRestartAndVerifiedCache(t *testing.T) {
	data := bytes.Repeat([]byte("valid-package-data"), 512)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.Write(data[:1200])
			return
		}
		if r.Header.Get("Range") != "bytes=1200-" {
			t.Errorf("expected retained offset, got %q", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 1200-%d/%d", len(data)-1, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[1200:])
	}))
	defer server.Close()
	release := resumeRelease(server.URL, data)
	opts := DownloadOptions{Directory: t.TempDir(), Resume: true}
	if _, err := resumeClient(t, server.URL).Download(context.Background(), release, opts); err == nil {
		t.Fatal("truncated response accepted")
	}
	path, err := resumeClient(t, server.URL).Download(context.Background(), release, opts)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, data) {
		t.Fatal("resumed bytes differ")
	}
	if _, err := resumeClient(t, server.URL).Download(context.Background(), release, opts); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("complete cache should avoid downloading again: %d calls", calls)
	}
}

func TestResumeIgnoresRangeAndRecoversInvalidRangesAndCorruptPrefix(t *testing.T) {
	for _, mode := range []string{"ignored", "bad-range", "416", "corrupt-prefix"} {
		t.Run(mode, func(t *testing.T) {
			data := bytes.Repeat([]byte("content"), 256)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Range") != "" && mode != "ignored" {
					if mode == "416" {
						w.WriteHeader(416)
						return
					}
					start := 100
					if mode == "bad-range" {
						start = 101
					}
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
					w.WriteHeader(206)
					w.Write(data[start:])
					return
				}
				w.Write(data)
			}))
			defer server.Close()
			release := resumeRelease(server.URL, data)
			dir := t.TempDir()
			work := filepath.Join(dir, "v"+release.Version+"-"+release.Artifact.SHA256)
			os.MkdirAll(work, 0700)
			sourceHash := sha256.Sum256([]byte(server.URL))
			prefix := bytes.Repeat([]byte("x"), 100)
			if mode != "corrupt-prefix" {
				prefix = data[:100]
			}
			os.WriteFile(filepath.Join(work, hex.EncodeToString(sourceHash[:16])+".part"), prefix, 0600)
			path, err := resumeClient(t, server.URL).Download(context.Background(), release, DownloadOptions{Directory: dir, Resume: true})
			if err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(path)
			if !bytes.Equal(got, data) {
				t.Fatal("invalid representation accepted")
			}
			if calls > 2 {
				t.Fatal("unbounded reset retries")
			}
		})
	}
}

func TestResumeKeepsCancelledBytesAndLockIsReleased(t *testing.T) {
	data := bytes.Repeat([]byte("a"), 256<<10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	defer server.Close()
	release := resumeRelease(server.URL, data)
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	_, err := resumeClient(t, server.URL).Download(ctx, release, DownloadOptions{Directory: dir, Resume: true, OnProgress: func(p Progress) {
		if p.Downloaded > 0 {
			cancel()
		}
	}})
	if err == nil {
		t.Fatal("cancellation accepted as successful completion")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*", "*.part"))
	if len(files) != 1 {
		t.Fatal("cancel discarded partial bytes")
	}
	if _, err := resumeClient(t, server.URL).Download(context.Background(), release, DownloadOptions{Directory: dir, Resume: true}); err != nil {
		t.Fatal(err)
	}
}

func TestResumeDoesNotMixMirrors(t *testing.T) {
	data := []byte(strings.Repeat("correct", 512))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.Write([]byte("bad"))
			return
		}
		if r.Header.Get("Range") != "" {
			t.Error("mixed bytes across sources")
		}
		w.Write(data)
	}))
	defer server.Close()
	r := resumeRelease(server.URL+"/bad", data)
	r.Artifact.Sources = append(r.Artifact.Sources, Source{Name: "backup", URL: server.URL + "/good"})
	if _, err := resumeClient(t, server.URL).Download(context.Background(), r, DownloadOptions{Directory: t.TempDir(), Resume: true}); err != nil {
		t.Fatal(err)
	}
}

func TestResumeLockPreventsTwoClientsAndFilenameCannotReplaceLock(t *testing.T) {
	data := []byte("a complete package")
	started := make(chan struct{})
	finish := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-finish; w.Write(data) }))
	defer server.Close()
	r := resumeRelease(server.URL, data)
	r.Artifact.FileName = ".download.lock"
	opts := DownloadOptions{Directory: t.TempDir(), Resume: true}
	done := make(chan error, 1)
	go func() { _, err := resumeClient(t, server.URL).Download(context.Background(), r, opts); done <- err }()
	<-started
	_, err := resumeClient(t, server.URL).Download(context.Background(), r, opts)
	if !errors.Is(err, ErrDownloadInProgress) {
		t.Errorf("cache was not locked across clients: %v", err)
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := resumeClient(t, server.URL).Download(context.Background(), r, opts); err != nil {
		t.Fatal("publisher filename damaged the lock or completed cache", err)
	}
}
