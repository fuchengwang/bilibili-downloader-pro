//go:build dev && !production && !bindings

package downloader

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDownloadTraceRecordsRangeConflictAndFailover(t *testing.T) {
	dir := t.TempDir()
	trace := startDownloadTrace(dir).forTask(&DownloadTask{ID: "task-1", BVID: "BVtest", CID: 42}, "run-1").forTrack("video")
	const total, prefix = 64 * 1024, 4096
	data := engineData(total)
	media := filepath.Join(dir, "private-filename.mp4")
	seedRangeRecord(t, media, total, 1, prefix)
	if err := os.WriteFile(media, append(append([]byte(nil), data[:prefix]...), make([]byte, total-prefix)...), 0600); err != nil {
		t.Fatal(err)
	}
	const primary = "https://primary.bilivideo.com/private-path?sign=private-signature"
	const backup = "https://backup.bilivideo.com/private-path?sign=private-signature"
	s := NewStreamDownloader([]string{primary, backup}, media)
	s.trace = trace
	s.skipSizeProbe = true
	s.client.Transport = routeFixture(
		streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: io.EOF}
		}),
		streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			body := data
			if r.URL.Hostname() == "primary.bilivideo.com" {
				body = data[:total/2]
			}
			resp := mockRangeMedia(r, body)
			resp.Header.Set("ETag", `"private-etag"`)
			resp.Header.Set("Set-Cookie", "SESSDATA=private-cookie")
			return resp, nil
		}),
	)
	if err := s.DownloadWithConcurrency(context.Background(), 4, nil); err != nil {
		t.Fatal(err)
	}
	assertMediaBytes(t, media, data)

	logPath := filepath.Join(trace.writer.dir, downloadTraceFile)
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-signature", "private-path", "private-cookie", "private-etag", "private-filename", "proxy.invalid"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("trace contains %q", secret)
		}
	}
	entries := readDownloadTrace(t, logPath)
	starts := make(map[float64]downloadTraceEntry)
	mismatchID := 0.0
	seenResponse, seenFallback, seenRejection, seenFinished := false, false, false, false
	for _, entry := range entries {
		if entry.Event == "session_start" {
			continue
		}
		if entry.TaskID != "task-1" || entry.RunID != "run-1" || entry.BVID != "BVtest" || entry.CID != 42 || entry.Track != "video" {
			t.Fatalf("unlinked trace entry: %+v", entry)
		}
		requestID, _ := entry.Fields["request_id"].(float64)
		switch entry.Event {
		case "request_start":
			starts[requestID] = entry
		case "route_fallback":
			seenFallback = seenFallback || entry.Fields["route"] == "direct" && entry.Fields["error"] == "eof"
		case "request_response":
			if entry.Fields["range_total"] == float64(total/2) {
				mismatchID = requestID
				seenResponse = entry.Fields["status"] == float64(http.StatusPartialContent) &&
					starts[requestID].Fields["expected_total"] == float64(total) &&
					starts[requestID].Fields["range"] == "bytes=4096-65535"
			}
		case "transfer_result":
			if requestID == mismatchID && (entry.Fields["validation_error"] != "entity_conflict" || entry.Fields["bytes_written"] != float64(0)) {
				t.Fatalf("mismatched bytes were not clearly rejected: %+v", entry)
			}
		case "source_rejected":
			seenRejection = entry.Fields["error"] == "entity_conflict"
		case "track_end":
			seenFinished = entry.Fields["error"] == ""
		case "checkpoint_reset":
			t.Fatal("healthy backup should not discard the checkpoint")
		}
	}
	if !seenResponse || !seenFallback || !seenRejection || !seenFinished {
		t.Fatalf("incomplete trace: response=%v fallback=%v rejection=%v finished=%v", seenResponse, seenFallback, seenRejection, seenFinished)
	}
}

func TestDownloadTraceRotationKeepsBoundedValidJSON(t *testing.T) {
	dir := t.TempDir()
	const limit = 700
	trace := downloadTrace{writer: &downloadTraceWriter{dir: dir, sessionID: "test", maxBytes: limit}}
	var writers sync.WaitGroup
	for i := range 8 {
		writers.Go(func() {
			for j := range 20 {
				trace.event("entry", map[string]any{"worker": i, "index": j, "padding": strings.Repeat("x", 140)})
			}
		})
	}
	writers.Wait()
	trace.event("last_entry", nil)
	files, err := filepath.Glob(filepath.Join(dir, downloadTraceFile+"*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != downloadTraceBackups+1 {
		t.Fatalf("rotation produced %d files", len(files))
	}
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > limit {
			t.Fatalf("%s exceeds the rotation limit: %d", path, info.Size())
		}
		readDownloadTrace(t, path)
	}
	current := readDownloadTrace(t, filepath.Join(dir, downloadTraceFile))
	if current[len(current)-1].Event != "last_entry" {
		t.Fatal("current log lost the latest event")
	}
}

func readDownloadTrace(t *testing.T, path string) []downloadTraceEntry {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := bufio.NewScanner(file)
	var entries []downloadTraceEntry
	for reader.Scan() {
		var entry downloadTraceEntry
		if err := json.Unmarshal(reader.Bytes(), &entry); err != nil {
			t.Fatalf("%s contains malformed JSONL: %v", path, err)
		}
		entries = append(entries, entry)
	}
	if err := reader.Err(); err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatalf("%s contains no events", path)
	}
	return entries
}
