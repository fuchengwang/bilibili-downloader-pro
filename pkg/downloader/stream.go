package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

// URLRefresher 动态刷新直链的回调函数 (当遇到 403 Forbidden 或鉴权过期时自动触发无缝刷新)
type URLRefresher func(ctx context.Context) ([]string, error)

// StreamDownloader 单个媒体流 (视频或音频) 的高可靠断点续传下载器
type StreamDownloader struct {
	diagnosticMu             sync.Mutex
	diagnosticLast           error
	onDiagnostic             func(error)
	client                   *http.Client
	urls                     []string
	targetPath               string
	totalSize                int64
	sizeHint                 int64 // parser estimate; never an integrity/completion length
	refresher                URLRefresher
	restoredProgressFn       DownloadProgressFn
	totalSizeFn              func(int64)
	bandwidth                *bandwidthLimiter
	slowRecoveries           atomic.Int32
	readPolicy               *streamReadPolicy
	acceleration             *streamAccelerationPolicy
	connections              connectionBudget
	probed                   bool
	skipSizeProbe            bool // Bilibili transfers discover lengths from actual range responses
	probeETag, probeModified string
	probeValidatorKey        string
	representation           string
	checkpoints              rangeStore
}

func (s *StreamDownloader) recordDiagnostic(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	if s.onDiagnostic != nil {
		s.onDiagnostic(err)
	}
	s.diagnosticMu.Lock()
	s.diagnosticLast = err
	s.diagnosticMu.Unlock()
}

func (s *StreamDownloader) lastDiagnostic() error {
	s.diagnosticMu.Lock()
	defer s.diagnosticMu.Unlock()
	return s.diagnosticLast
}

// SetURLRefresher 配置直链鉴权失效时的动态刷新器
func (s *StreamDownloader) SetURLRefresher(fn URLRefresher) {
	s.refresher = fn
}

// Restored bytes contribute to completion, but are not new network traffic.
// With no separate callback, retain the legacy combined progress behavior.
func (s *StreamDownloader) SetRestoredProgressCallback(fn DownloadProgressFn) {
	s.restoredProgressFn = fn
}

func (s *StreamDownloader) SetTotalSizeCallback(fn func(int64)) {
	s.totalSizeFn = fn
}

func (s *StreamDownloader) setTotalSize(size int64) {
	s.totalSize = size
	if s.totalSizeFn != nil {
		s.totalSizeFn(size)
	}
}

// NewStreamDownloader 创建单流下载器 (支持多候选 CDN 节点与长连接 Transport)
func NewStreamDownloader(urls []string, targetPath string) *StreamDownloader {
	if len(urls) == 0 {
		urls = []string{""}
	}

	return &StreamDownloader{
		client: &http.Client{
			Transport: mediaTransportForStream(),
			Timeout:   0, // 大文件流式下载必须为 0，由 Context 控制生命周期
		},
		urls:        append([]string(nil), urls...),
		connections: globalConnections,
		targetPath:  targetPath,
	}
}

// parseContentRange 解析 "bytes <start>-<end>/<total>"，也支持 416 使用的 "bytes */<total>"。
func parseContentRange(cr string) (start, end, total int64, err error) {
	cr = strings.TrimSpace(cr)
	if len(cr) < len("bytes ") || !strings.EqualFold(cr[:len("bytes ")], "bytes ") {
		return 0, 0, 0, fmt.Errorf("invalid content-range unit: %s", cr)
	}
	cr = strings.TrimSpace(cr[len("bytes "):])
	parts := strings.Split(cr, "/")
	if len(parts) != 2 {
		return 0, 0, 0, fmt.Errorf("invalid content-range: %s", cr)
	}

	total = -1
	totalPart := strings.TrimSpace(parts[1])
	if totalPart != "*" {
		total, err = strconv.ParseInt(totalPart, 10, 64)
		if err != nil || total < 0 {
			return 0, 0, 0, fmt.Errorf("invalid content-range total: %s", parts[1])
		}
	}

	rangePart := strings.TrimSpace(parts[0])
	if rangePart == "*" {
		// 416 Unsatisfied Range 的合法形式是 bytes */<total>。
		if total < 0 {
			return 0, 0, 0, fmt.Errorf("invalid unsatisfied content-range total: %s", cr)
		}
		return -1, -1, total, nil
	}

	rangeParts := strings.Split(rangePart, "-")
	if len(rangeParts) != 2 {
		return 0, 0, 0, fmt.Errorf("invalid range parts: %s", parts[0])
	}
	start, err1 := strconv.ParseInt(strings.TrimSpace(rangeParts[0]), 10, 64)
	end, err2 := strconv.ParseInt(strings.TrimSpace(rangeParts[1]), 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, 0, fmt.Errorf("invalid range numbers: %s", parts[0])
	}
	if start < 0 || end < start {
		return 0, 0, 0, fmt.Errorf("invalid range bounds: %s", parts[0])
	}
	if total >= 0 && end >= total {
		return 0, 0, 0, fmt.Errorf("range exceeds total: %s", cr)
	}
	return start, end, total, nil
}

func isLikelyNonMediaResponse(resp *http.Response) bool {
	if resp == nil {
		return true
	}
	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	return strings.HasPrefix(contentType, "text/html") ||
		strings.HasPrefix(contentType, "application/xhtml+xml") ||
		strings.HasPrefix(contentType, "application/json") ||
		strings.HasPrefix(contentType, "text/json")
}

// GetTotalSize 获取流的总大小 (使用带 Range: bytes=0-0 请求探测)
func (s *StreamDownloader) GetTotalSize(ctx context.Context) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.totalSize > 0 {
		return s.totalSize, nil
	}
	return s.probeTotalSize(ctx)
}

// A completed cache must get fresh headers, even if its size is already known.
func (s *StreamDownloader) probeTotalSize(ctx context.Context) (int64, error) {
	s.probed = true
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	var lastErr error
	for i := 0; i < len(s.urls); i++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		urlToProbe := s.urls[i]
		if urlToProbe == "" {
			continue
		}

		candidateCtx, stopCandidate := context.WithTimeout(ctx, 2*time.Second)
		defer stopCandidate()
		req, err := http.NewRequestWithContext(candidateCtx, http.MethodGet, urlToProbe, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", bilibili.BrowserUA)
		req.Header.Set("Referer", bilibili.Referer)
		req.Header.Set("Range", "bytes=0-0")
		req.Header.Set("Accept-Encoding", "identity")

		resp, err := s.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			s.recordDiagnostic(err)
			lastErr = err
			continue
		}
		if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("探测响应使用了不支持的内容编码")
			continue
		}

		// 处理 206 Partial Content：必须严格依赖 Content-Range 头计算文件总大小，严禁读取仅代表分片大小的 Content-Length
		if resp.StatusCode == http.StatusPartialContent {
			if isLikelyNonMediaResponse(resp) {
				_ = resp.Body.Close()
				lastErr = fmt.Errorf("探测响应不是媒体内容: %s", resp.Header.Get("Content-Type"))
				continue
			}
			cr := resp.Header.Get("Content-Range")
			first, last, total, pErr := parseContentRange(cr)
			if pErr == nil && first == 0 && last == 0 && total > 0 && (resp.ContentLength < 0 || resp.ContentLength == 1) {
				_ = resp.Body.Close()
				s.rememberProbeValidators(resp)
				s.setTotalSize(total)
				s.preferProbeURL(i)
				return total, nil
			}
			_ = resp.Body.Close()
			if pErr != nil {
				lastErr = fmt.Errorf("探测响应 Content-Range 无效 (%q): %w", cr, pErr)
			} else {
				lastErr = fmt.Errorf("探测响应 Content-Range 缺少有效总长度 (%q)", cr)
			}
			continue
		}

		// 处理 200 OK：服务端未按 Range 响应而是返回整个流实体，此时方可读取 Content-Length
		if resp.StatusCode == http.StatusOK {
			if isLikelyNonMediaResponse(resp) {
				_ = resp.Body.Close()
				lastErr = fmt.Errorf("探测响应不是媒体内容: %s", resp.Header.Get("Content-Type"))
				continue
			}
			if resp.ContentLength > 0 {
				_ = resp.Body.Close()
				s.rememberProbeValidators(resp)
				s.setTotalSize(resp.ContentLength)
				s.preferProbeURL(i)
				return resp.ContentLength, nil
			}
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("探测响应缺少有效 Content-Length")
			continue
		}

		// 所有探测响应都必须关闭，即使状态码不是 200/206；否则长任务会耗尽连接与文件描述符。
		_ = resp.Body.Close()
		lastErr = fmt.Errorf("探测 URL 返回异常: %s", resp.Status)
	}

	if lastErr != nil {
		return 0, lastErr
	}
	return 0, fmt.Errorf("无有效下载 URL")
}

func (s *StreamDownloader) rememberProbeValidators(resp *http.Response) {
	s.probeETag, s.probeModified = resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	s.probeValidatorKey = ""
	if resp.Request != nil {
		s.probeValidatorKey = mediaValidatorKey(resp.Request.URL.String())
	}
}

func (s *StreamDownloader) preferProbeURL(index int) {
	if index > 0 {
		url := s.urls[index]
		copy(s.urls[1:index+1], s.urls[:index])
		s.urls[0] = url
	}
}

// DownloadProgressFn 进度更新回调
type DownloadProgressFn func(downloadedDelta int64)

// DownloadWithConcurrency 统一的高性能高可靠流式下载器接口
func (s *StreamDownloader) DownloadWithConcurrency(ctx context.Context, concurrency int, progressFn DownloadProgressFn) error {
	return s.downloadCore(ctx, concurrency, progressFn)
}

func streamSegmentPath(targetPath string, index int) string {
	return fmt.Sprintf("%s.part-%02d", targetPath, index)
}

func filterStreamURLs(urls []string) []string {
	filtered := make([]string, 0, len(urls))
	seen := make(map[string]bool)
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u != "" && !seen[u] {
			seen[u] = true
			filtered = append(filtered, u)
		}
	}
	return filtered
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Repeated connection failures often belong to the same brief outage. Give
// the route time to recover rather than burning all fifteen tries in seconds.
// The existing retry count remains bounded and cancellation interrupts the wait.
func (s *StreamDownloader) DownloadSingleStream(ctx context.Context, progressFn DownloadProgressFn) error {
	return s.downloadCore(ctx, 1, progressFn)
}
