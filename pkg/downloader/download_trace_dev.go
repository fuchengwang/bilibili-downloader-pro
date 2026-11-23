//go:build dev && !production && !bindings

package downloader

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

const (
	downloadTraceEnabled = true
	downloadTraceFile    = "download.jsonl"
	downloadTraceLimit   = 4 << 20
	downloadTraceBackups = 3
)

type downloadTrace struct {
	writer *downloadTraceWriter
	taskID string
	runID  string
	bvid   string
	cid    int64
	track  string
}

type downloadTraceWriter struct {
	mu        sync.Mutex
	dir       string
	sessionID string
	maxBytes  int64
	requests  atomic.Uint64
	stopped   bool
}

type downloadTraceEntry struct {
	Time      time.Time      `json:"time"`
	SessionID string         `json:"session_id"`
	Event     string         `json:"event"`
	TaskID    string         `json:"task_id,omitempty"`
	RunID     string         `json:"run_id,omitempty"`
	BVID      string         `json:"bvid,omitempty"`
	CID       int64          `json:"cid,omitempty"`
	Track     string         `json:"track,omitempty"`
	Fields    map[string]any `json:"fields,omitempty"`
}

func startDownloadTrace(configDir string) downloadTrace {
	dir := filepath.Join(configDir, "dev-download-logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		log.Printf("打开开发下载日志目录失败: %v", err)
		return downloadTrace{}
	}
	t := downloadTrace{writer: &downloadTraceWriter{dir: dir, sessionID: newUUID(), maxBytes: downloadTraceLimit}}
	t.event("session_start", map[string]any{"schema": 1, "pid": os.Getpid()})
	if !t.writer.stopped {
		log.Printf("开发下载日志 %s", filepath.Join(dir, downloadTraceFile))
	}
	return t
}

func (t downloadTrace) active() bool { return t.writer != nil }

func (t downloadTrace) forTask(task *DownloadTask, token string) downloadTrace {
	t.taskID, t.runID, t.bvid, t.cid = task.ID, token, task.BVID, task.CID
	return t
}

func (t downloadTrace) forTrack(name string) downloadTrace {
	t.track = name
	return t
}

func (t downloadTrace) event(name string, fields map[string]any) {
	if !t.active() {
		return
	}
	t.writer.append(downloadTraceEntry{
		Time: time.Now().UTC(), SessionID: t.writer.sessionID, Event: name,
		TaskID: t.taskID, RunID: t.runID, BVID: t.bvid, CID: t.cid, Track: t.track, Fields: fields,
	})
}

// Called before any range worker starts or after all of them have joined.
func (e *rangeTransfer) traceCheckpoint(name string, err error) {
	e.s.trace.event(name, map[string]any{
		"bytes_on_disk": e.received(), "total_bytes": e.total, "range_count": len(e.ranges),
		"ranges":    e.ranges[:min(len(e.ranges), 128)],
		"etag_hash": traceFingerprint(e.etag), "last_modified_hash": traceFingerprint(e.modified),
		"threads": e.threads, "sequential": e.sequential, "error": traceError(err),
	})
}

func (w *downloadTraceWriter) append(entry downloadTraceEntry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	if err := w.write(entry); err != nil {
		w.stopped = true
		log.Printf("写入开发下载日志失败: %v", err)
	}
}

func (w *downloadTraceWriter) write(entry downloadTraceEntry) error {
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if int64(len(line)) > w.maxBytes {
		return fmt.Errorf("单条下载日志超出文件大小上限")
	}
	path := filepath.Join(w.dir, downloadTraceFile)
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("下载日志路径不是普通文件")
		}
		if info.Size()+int64(len(line)) > w.maxBytes {
			if err := rotateDownloadTrace(path); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(line)
	return errors.Join(err, f.Close())
}

func rotateDownloadTrace(path string) error {
	for i := downloadTraceBackups; i > 0; i-- {
		older := path + "." + strconv.Itoa(i)
		newer := path
		if i > 1 {
			newer += "." + strconv.Itoa(i-1)
		}
		// On Windows, Rename cannot replace a file already at the destination.
		if err := os.Remove(older); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(newer, older); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

type downloadTraceRequest struct {
	trace downloadTrace
	id    uint64
	start time.Time
}

func (t downloadTrace) request(req *http.Request, phase string, fields map[string]any) downloadTraceRequest {
	if !t.active() {
		return downloadTraceRequest{}
	}
	r := downloadTraceRequest{trace: t, id: t.writer.requests.Add(1), start: time.Now()}
	if fields == nil {
		fields = make(map[string]any)
	}
	fields["phase"] = phase
	fields["source"] = traceSource(req.URL.String())
	fields["range"] = req.Header.Get("Range")
	fields["if_range_hash"] = traceFingerprint(req.Header.Get("If-Range"))
	r.event("request_start", fields)
	return r
}

func (r downloadTraceRequest) event(name string, fields map[string]any) {
	if !r.trace.active() {
		return
	}
	fields["request_id"] = r.id
	fields["elapsed_ms"] = time.Since(r.start).Milliseconds()
	r.trace.event(name, fields)
}

func (r downloadTraceRequest) response(resp *http.Response, err error) {
	if !r.trace.active() {
		return
	}
	fields := map[string]any{"error": traceError(err)}
	if resp != nil {
		fields["status"] = resp.StatusCode
		fields["content_length"] = resp.ContentLength
		fields["etag_hash"] = traceFingerprint(resp.Header.Get("ETag"))
		fields["last_modified_hash"] = traceFingerprint(resp.Header.Get("Last-Modified"))
		fields["non_media_content_type"] = isLikelyNonMediaResponse(resp)
		encoding := resp.Header.Get("Content-Encoding")
		fields["non_identity_encoding"] = encoding != "" && !strings.EqualFold(encoding, "identity")
		if raw := resp.Header.Get("Content-Range"); raw != "" {
			start, end, total, err := parseContentRange(raw)
			fields["content_range_valid"] = err == nil
			if err == nil {
				fields["range_start"], fields["range_end"], fields["range_total"] = start, end, total
			}
		}
		if resp.Header.Get("Retry-After") != "" {
			fields["retry_after_ms"] = retryAfter(resp.Header).Milliseconds()
		}
		if resp.Request != nil {
			fields["response_source"] = traceSource(resp.Request.URL.String())
		}
	}
	r.event("request_response", fields)
}

type downloadTraceRequestKey struct{}

func (r downloadTraceRequest) withContext(req *http.Request) *http.Request {
	if !r.trace.active() {
		return req
	}
	return req.WithContext(context.WithValue(req.Context(), downloadTraceRequestKey{}, r))
}

func traceMediaRoute(req *http.Request, event, route string, err error) {
	if r, ok := req.Context().Value(downloadTraceRequestKey{}).(downloadTraceRequest); ok {
		r.event(event, map[string]any{"route": route, "error": traceError(err)})
	}
}

type downloadTraceSource struct {
	Host string `json:"host,omitempty"`
	ID   string `json:"id,omitempty"`
}

func traceSource(raw string) downloadTraceSource {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return downloadTraceSource{}
	}
	return downloadTraceSource{Host: u.Hostname(), ID: traceFingerprint(u.Scheme + "://" + u.Host + u.EscapedPath())}
}

func traceSources(urls []string) []downloadTraceSource {
	list := make([]downloadTraceSource, 0, len(urls))
	for _, u := range urls {
		list = append(list, traceSource(u))
	}
	return list
}

func traceFingerprint(raw string) string {
	if raw == "" {
		return ""
	}
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:8])
}

// Do not stringify arbitrary errors: wrapped errors may contain signed URLs,
// account data, local paths, or response text returned by a remote server.
func traceError(err error) string {
	var status *mediaHTTPError
	var playback *bilibili.PlaybackError
	var dns *net.DNSError
	var network *net.OpError
	var cert x509.UnknownAuthorityError
	var file *os.PathError
	var errno syscall.Errno
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, errEntityChanged):
		return "entity_conflict"
	case errors.Is(err, errRangeUnsupported):
		return "range_unsupported"
	case errors.Is(err, errAllSourcesRejected):
		return "all_sources_rejected"
	case errors.Is(err, errNonMediaResponse):
		return "non_media_response"
	case errors.Is(err, errUnsupportedEncoding):
		return "unsupported_encoding"
	case errors.Is(err, errIdleStream):
		return "read_idle"
	case errors.Is(err, errSlowStream):
		return "slow_stream"
	case errors.As(err, &status):
		return "http_" + strconv.Itoa(status.code)
	case errors.As(err, &playback):
		return "playback_" + playback.Kind
	case errors.As(err, &dns):
		return "dns"
	case errors.As(err, &cert):
		return "untrusted_certificate"
	case errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected_eof"
	case errors.Is(err, io.EOF):
		return "eof"
	case isDiskSpaceError(err):
		return "disk_full"
	case errors.As(err, &file):
		return "file_" + file.Op
	case errors.As(err, &network):
		if errors.As(network.Err, &errno) {
			return "network_" + network.Op + "_errno_" + strconv.Itoa(int(errno))
		}
		if network.Timeout() {
			return "network_" + network.Op + "_timeout"
		}
		return "network_" + network.Op
	case errors.As(err, &errno):
		return "errno_" + strconv.Itoa(int(errno))
	default:
		return "other"
	}
}
