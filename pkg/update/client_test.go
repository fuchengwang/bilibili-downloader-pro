package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	c, err := New(Config{ServerURL: server.URL, AppID: "test-app", CurrentVersion: "1.0.0", OS: "darwin", Arch: "arm64"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sendResult(w http.ResponseWriter, release *Release) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Response[CheckResult]{Code: 200, Success: true, Data: CheckResult{AppID: "test-app", CurrentVersion: "1.0.0", HasUpdate: release != nil, Release: release}})
}

func testRelease(base string, data []byte) Release {
	hash := sha256.Sum256(data)
	return Release{Version: "1.1.0", Artifact: Artifact{OS: "darwin", Arch: "universal", FileName: "tool.zip", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:]), Sources: []Source{{Name: "primary", URL: base + "/file"}}}}
}

func TestCheckProtocolAndErrors(t *testing.T) {
	mode := "ok"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("app_id") != "test-app" || r.URL.Query().Get("arch") != "arm64" {
			t.Error("query parameters not encoded")
		}
		switch mode {
		case "ok":
			sendResult(w, nil)
		case "bad-json":
			_, _ = w.Write([]byte("<html>login</html>"))
		case "server":
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{"success":false,"code":500,"message":"unavailable"}`))
		case "business":
			_, _ = w.Write([]byte(`{"success":false,"code":400,"message":"bad request"}`))
		case "mismatch":
			_, _ = w.Write([]byte(`{"success":true,"code":200,"data":{"app_id":"other","current_version":"1.0.0","has_update":false}}`))
		case "missing-status":
			_, _ = w.Write([]byte(`{"success":true,"code":200,"data":{"app_id":"test-app","current_version":"1.0.0"}}`))
		}
	}))
	defer ts.Close()
	c := testClient(t, ts)
	for _, value := range []string{"ok", "bad-json", "server", "business", "mismatch", "missing-status"} {
		mode = value
		result, err := c.Check(context.Background())
		if value == "ok" {
			if err != nil || result.HasUpdate {
				t.Fatalf("no update: %v", err)
			}
		} else if err == nil || result != nil {
			t.Fatalf("%s must be an error, not no update", value)
		}
	}
}

func TestCheckCoalescesAndWaitingCallerCanCancel(t *testing.T) {
	var requests atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		close(entered)
		<-release
		sendResult(w, nil)
	}))
	defer ts.Close()
	c := testClient(t, ts)
	first := make(chan error, 1)
	go func() { _, err := c.Check(context.Background()); first <- err }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() { _, err := c.Check(ctx); second <- err }()
	cancel()
	if err := <-second; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting cancellation: %v", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal("concurrent requests were duplicated")
	}
}

func TestRateLimitSuppressesManualAndAutomaticRequests(t *testing.T) {
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"success":false,"code":429,"data":{"retry_after":2}}`))
	}))
	defer ts.Close()
	c := testClient(t, ts)
	for i := 0; i < 3; i++ {
		if _, err := c.Check(context.Background()); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("rate limit: %v", err)
		}
	}
	if requests.Load() != 1 {
		t.Fatal("cooldown was not respected")
	}
	policy := Schedule{CheckOnStartup: true, Interval: time.Millisecond, StartupDelay: time.Nanosecond, StatePath: filepath.Join(t.TempDir(), "state.json")}
	m, err := c.Start(context.Background(), policy, func(*CheckResult, error) {})
	if err != nil {
		t.Fatal(err)
	}
	m.Stop()
	<-m.Done()
	if requests.Load() != 1 {
		t.Fatal("automatic checks bypassed cooldown")
	}
	if retryAfter(http.TimeFormat, time.Now()) != 0 {
		t.Fatal("bad retry-after accepted")
	}
	if retryAfter(time.Now().Add(time.Minute).UTC().Format(http.TimeFormat), time.Now()) <= 0 {
		t.Fatal("HTTP date cooldown not understood")
	}
}

func TestDownloadFallbackValidationAndCleanup(t *testing.T) {
	data := []byte("complete package contents")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/corrupt":
			_, _ = w.Write([]byte("xxxxxxxxxxxxxxxxxxxxxxxxx"))
		case "/missing":
			w.WriteHeader(404)
		default:
			_, _ = w.Write(data)
		}
	}))
	defer ts.Close()
	c := testClient(t, ts)
	release := testRelease(ts.URL, data)
	release.Artifact.Sources = []Source{{URL: ts.URL + "/missing"}, {URL: ts.URL + "/corrupt"}, {URL: ts.URL + "/file"}}
	directory := t.TempDir()
	var indexes []int
	path, err := c.Download(context.Background(), release, DownloadOptions{Directory: directory, OnProgress: func(p Progress) {
		if p.Downloaded == 0 {
			indexes = append(indexes, p.SourceIndex)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	if string(content) != string(data) || len(indexes) != 3 {
		t.Fatal("mirror fallback did not deliver the verified file")
	}
	if files, _ := os.ReadDir(filepath.Dir(path)); len(files) != 1 {
		t.Fatal("partial file remains")
	}
	for _, bad := range []string{"../tool.zip", `C:\tool.zip`, ".", "..", "bad\n.zip"} {
		r := release
		r.Artifact.FileName = bad
		if _, err := c.Download(context.Background(), r, DownloadOptions{Directory: directory}); err == nil {
			t.Fatalf("unsafe filename %q accepted", bad)
		}
	}
	release.Artifact.Sources = release.Artifact.Sources[:2]
	if _, err := c.Download(context.Background(), release, DownloadOptions{Directory: directory}); err == nil {
		t.Fatal("corrupt sources accepted")
	}
	if files, _ := os.ReadDir(directory); len(files) != 1 {
		t.Fatal("failed download directories remain")
	}
	if content, _ := os.ReadFile(path); string(content) != string(data) {
		t.Fatal("previous download was modified")
	}
}

func TestDownloadCancelAndDuplicate(t *testing.T) {
	entered := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done() }))
	defer ts.Close()
	c := testClient(t, ts)
	release := testRelease(ts.URL, []byte("package"))
	directory := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Download(ctx, release, DownloadOptions{Directory: directory}); done <- err }()
	<-entered
	if _, err := c.Download(context.Background(), release, DownloadOptions{Directory: directory}); !errors.Is(err, ErrDownloadInProgress) {
		t.Fatalf("duplicate download: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if files, _ := os.ReadDir(directory); len(files) != 0 {
		t.Fatal("canceled files remain")
	}
}

func TestReleaseCannotDowngradeOrChangePlatform(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sendResult(w, nil) }))
	defer ts.Close()
	c := testClient(t, ts)
	r := testRelease(ts.URL, []byte("package"))
	if err := c.Confirm(context.Background(), r); !errors.Is(err, ErrReleaseChanged) {
		t.Fatalf("withdrawn release: %v", err)
	}
	for _, mutate := range []func(*Release){
		func(r *Release) { r.Version = "0.9.0" },
		func(r *Release) { r.Artifact.OS = "windows" },
		func(r *Release) { r.Artifact.SHA256 = "invalid" },
		func(r *Release) { r.Artifact.Size = 0 },
		func(r *Release) { r.Artifact.Sources[0].URL = "file:///tmp/a" },
	} {
		r := testRelease(ts.URL, []byte("package"))
		mutate(&r)
		if err := c.validateRelease(r); err == nil {
			t.Fatal("invalid release was accepted")
		}
	}
}
