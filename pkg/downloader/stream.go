package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

// URLRefresher 动态刷新直链的回调函数 (当遇到 403 Forbidden 或鉴权过期时自动触发无缝刷新)
type URLRefresher func(ctx context.Context) ([]string, error)

// StreamDownloader 单个媒体流 (视频或音频) 的高可靠断点续传下载器
type StreamDownloader struct {
	client     *http.Client
	urls       []string
	urlIdx     int
	targetPath string
	totalSize  int64
	refresher  URLRefresher
}

// SetURLRefresher 配置直链鉴权失效时的动态刷新器
func (s *StreamDownloader) SetURLRefresher(fn URLRefresher) {
	s.refresher = fn
}

// NewStreamDownloader 创建单流下载器 (支持多候选 CDN 节点与长连接 Transport)
func NewStreamDownloader(urls []string, targetPath string) *StreamDownloader {
	if len(urls) == 0 {
		urls = []string{""}
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &StreamDownloader{
		client: &http.Client{
			Transport: transport,
			Timeout:   0, // 大文件流式下载必须为 0，由 Context 控制生命周期
		},
		urls:       urls,
		urlIdx:     0,
		targetPath: targetPath,
	}
}

func (s *StreamDownloader) currentURL() string {
	if len(s.urls) == 0 {
		return ""
	}
	return s.urls[s.urlIdx%len(s.urls)]
}

func (s *StreamDownloader) rotateURL() {
	if len(s.urls) > 1 {
		s.urlIdx = (s.urlIdx + 1) % len(s.urls)
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

	var lastErr error
	for i := 0; i < len(s.urls); i++ {
		urlToProbe := s.urls[(s.urlIdx+i)%len(s.urls)]
		if urlToProbe == "" {
			continue
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlToProbe, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", bilibili.BrowserUA)
		req.Header.Set("Referer", bilibili.Referer)
		req.Header.Set("Range", "bytes=0-0")

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = err
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
			_, _, total, pErr := parseContentRange(cr)
			if pErr == nil && total > 0 {
				_ = resp.Body.Close()
				s.totalSize = total
				s.urlIdx = (s.urlIdx + i) % len(s.urls)
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
				s.totalSize = resp.ContentLength
				s.urlIdx = (s.urlIdx + i) % len(s.urls)
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

// DownloadProgressFn 进度更新回调
type DownloadProgressFn func(downloadedDelta int64)

// DownloadWithConcurrency 统一的高性能高可靠流式下载器接口
func (s *StreamDownloader) DownloadWithConcurrency(ctx context.Context, concurrency int, progressFn DownloadProgressFn) error {
	if strings.TrimSpace(s.targetPath) == "" {
		return fmt.Errorf("目标文件路径为空")
	}
	if concurrency <= 1 {
		return s.DownloadSingleStream(ctx, progressFn)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(filepath.Dir(s.targetPath), 0755); err != nil {
		return fmt.Errorf("创建下载目录失败: %w", err)
	}

	if s.totalSize <= 0 {
		if _, err := s.GetTotalSize(ctx); err != nil {
			return err
		}
	}
	if s.totalSize <= 0 {
		return fmt.Errorf("并发下载需要服务端提供可验证的总长度")
	}

	if concurrency > 8 {
		concurrency = 8
	}
	if s.totalSize < int64(concurrency) {
		concurrency = int(s.totalSize)
	}
	if concurrency <= 1 {
		return s.DownloadSingleStream(ctx, progressFn)
	}

	initialSize := int64(0)
	if info, err := os.Stat(s.targetPath); err == nil {
		initialSize = info.Size()
		if initialSize == s.totalSize {
			removeSegmentPartFiles(s.targetPath, concurrency)
			return nil
		}
		if initialSize > s.totalSize {
			if err := os.Remove(s.targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("清理过大的临时文件失败: %w", err)
			}
			if progressFn != nil {
				progressFn(-initialSize)
			}
			initialSize = 0
		}
	}

	segments := make([]streamSegment, concurrency)
	baseSegmentSize := s.totalSize / int64(concurrency)
	remaining := s.totalSize % int64(concurrency)
	var offset int64
	for i := range segments {
		length := baseSegmentSize
		if int64(i) < remaining {
			length++
		}
		segments[i] = streamSegment{
			index: i,
			start: offset,
			end:   offset + length - 1,
			path:  streamSegmentPath(s.targetPath, i),
		}
		offset += length
	}

	var progressMu sync.Mutex
	var parallelReported int64
	reportProgress := func(delta int64) {
		if delta == 0 {
			return
		}
		progressMu.Lock()
		parallelReported += delta
		if progressFn != nil {
			progressFn(delta)
		}
		progressMu.Unlock()
	}

	for i := range segments {
		segment := &segments[i]
		if err := prepareSegmentPart(ctx, segment, initialSize, reportProgress); err != nil {
			progressMu.Lock()
			reported := parallelReported
			progressMu.Unlock()
			if reported > 0 {
				reportProgress(-reported)
			}
			removeSegmentPartFiles(s.targetPath, concurrency)
			return err
		}
	}

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	recordError := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil || (errors.Is(err, errRangeUnsupported) && !errors.Is(firstErr, errRangeUnsupported)) {
			firstErr = err
		}
		errMu.Unlock()
		cancel()
	}

	for i := range segments {
		segment := segments[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.downloadSegment(workCtx, &segment, reportProgress); err != nil {
				recordError(err)
			}
		}()
	}
	wg.Wait()

	if ctx.Err() != nil {
		return ctx.Err()
	}
	errMu.Lock()
	parallelErr := firstErr
	errMu.Unlock()
	if parallelErr != nil {
		if errors.Is(parallelErr, errRangeUnsupported) {
			progressMu.Lock()
			reported := parallelReported
			progressMu.Unlock()
			if reported > 0 {
				reportProgress(-reported)
			}
			removeSegmentPartFiles(s.targetPath, concurrency)
			return s.DownloadSingleStream(ctx, progressFn)
		}
		return parallelErr
	}

	if err := assembleSegmentParts(ctx, s.targetPath, segments); err != nil {
		return err
	}
	removeSegmentPartFiles(s.targetPath, concurrency)
	return nil
}

type streamSegment struct {
	index int
	start int64
	end   int64
	path  string
}

var errRangeUnsupported = errors.New("server does not support ranged downloads")

func streamSegmentPath(targetPath string, index int) string {
	return fmt.Sprintf("%s.part-%02d", targetPath, index)
}

func prepareSegmentPart(ctx context.Context, segment *streamSegment, targetSize int64, reportProgress DownloadProgressFn) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	expectedSize := segment.end - segment.start + 1
	info, err := os.Stat(segment.path)
	if err == nil {
		if info.IsDir() || info.Size() > expectedSize {
			if err := os.Remove(segment.path); err != nil {
				return fmt.Errorf("清理异常分片文件失败: %w", err)
			}
			info = nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	partSize := int64(0)
	if info != nil {
		partSize = info.Size()
	}
	knownTargetEnd := targetSize
	if knownTargetEnd > segment.end+1 {
		knownTargetEnd = segment.end + 1
	}
	if knownTargetEnd < segment.start {
		knownTargetEnd = segment.start
	}
	targetPartSize := knownTargetEnd - segment.start
	if targetPartSize < 0 {
		targetPartSize = 0
	}
	if partSize < knownTargetEnd-segment.start {
		if err := appendTargetPrefixContext(ctx, segment, partSize, knownTargetEnd-segment.start-partSize); err != nil {
			return err
		}
		partSize = knownTargetEnd - segment.start
	}
	if partSize > expectedSize {
		return fmt.Errorf("分片文件大小超出范围: %s", segment.path)
	}

	accounted := targetPartSize
	if accounted > partSize {
		accounted = partSize
	}
	if partSize > accounted && reportProgress != nil {
		reportProgress(partSize - accounted)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func appendTargetPrefix(segment *streamSegment, partSize, count int64) error {
	return appendTargetPrefixContext(context.Background(), segment, partSize, count)
}

func appendTargetPrefixContext(ctx context.Context, segment *streamSegment, partSize, count int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if count <= 0 {
		return nil
	}
	targetPath := strings.TrimSuffix(segment.path, fmt.Sprintf(".part-%02d", segment.index))
	src, err := os.Open(targetPath)
	if err != nil {
		return fmt.Errorf("打开已有临时文件失败: %w", err)
	}
	defer src.Close()
	if _, err := src.Seek(segment.start+partSize, io.SeekStart); err != nil {
		return err
	}
	dst, err := os.OpenFile(segment.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("打开并发分片文件失败: %w", err)
	}
	defer dst.Close()
	written, err := copyExactContext(ctx, src, dst, count)
	if err != nil {
		return err
	}
	if written != count {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func copyExact(src io.Reader, dst io.Writer, count int64) (int64, error) {
	return copyExactContext(context.Background(), src, dst, count)
}

func copyExactContext(ctx context.Context, src io.Reader, dst io.Writer, count int64) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	buf := make([]byte, 128*1024)
	var written int64
	for written < count {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		want := int64(len(buf))
		if remaining := count - written; want > remaining {
			want = remaining
		}
		n, err := src.Read(buf[:want])
		if n > 0 {
			if writeErr := writeAll(dst, buf[:n]); writeErr != nil {
				return written, writeErr
			}
			written += int64(n)
		}
		if err != nil {
			if err == io.EOF && written == count {
				break
			}
			return written, err
		}
		if n == 0 {
			return written, io.ErrNoProgress
		}
	}
	return written, nil
}

func writeAll(dst io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := dst.Write(data)
		if written < 0 || written > len(data) {
			return io.ErrShortWrite
		}
		if written > 0 {
			data = data[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func (s *StreamDownloader) downloadSegment(ctx context.Context, segment *streamSegment, progressFn DownloadProgressFn) error {
	urls := append([]string(nil), s.urls...)
	if len(urls) == 0 {
		return fmt.Errorf("无有效下载 URL")
	}
	urlIdx := 0
	const maxSegmentRetries = 15
	var lastErr error
	for attempt := 0; attempt < maxSegmentRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		partInfo, err := os.Stat(segment.path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		partSize := int64(0)
		if err == nil {
			partSize = partInfo.Size()
		}
		expectedSize := segment.end - segment.start + 1
		if partSize == expectedSize {
			return nil
		}
		requestStart := segment.start + partSize
		if requestStart > segment.end {
			return fmt.Errorf("分片起点超出范围: %d > %d", requestStart, segment.end)
		}
		if urlIdx >= len(urls) {
			urlIdx = 0
		}
		currentURL := strings.TrimSpace(urls[urlIdx])
		if currentURL == "" {
			urlIdx = (urlIdx + 1) % len(urls)
			continue
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, currentURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", bilibili.BrowserUA)
		req.Header.Set("Referer", bilibili.Referer)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", requestStart, segment.end))
		resp, err := s.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			urlIdx = (urlIdx + 1) % len(urls)
			if err := waitForRetry(ctx, 300*time.Millisecond); err != nil {
				return err
			}
			continue
		}

		if resp.StatusCode == http.StatusForbidden {
			_ = resp.Body.Close()
			if s.refresher != nil {
				if refreshed, refreshErr := s.refresher(ctx); refreshErr == nil && len(refreshed) > 0 {
					failedURL := currentURL
					urls = filterStreamURLs(refreshed)
					if len(urls) == 0 {
						return fmt.Errorf("刷新后无有效下载 URL")
					}
					urlIdx = chooseRefreshedURLIndex(urls, failedURL)
					continue
				}
			}
			urlIdx = (urlIdx + 1) % len(urls)
			if err := waitForRetry(ctx, 500*time.Millisecond); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode >= 500 {
			_ = resp.Body.Close()
			urlIdx = (urlIdx + 1) % len(urls)
			if err := waitForRetry(ctx, 500*time.Millisecond); err != nil {
				return err
			}
			continue
		}
		if resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			return errRangeUnsupported
		}
		if resp.StatusCode != http.StatusPartialContent {
			_ = resp.Body.Close()
			urlIdx = (urlIdx + 1) % len(urls)
			if err := waitForRetry(ctx, 300*time.Millisecond); err != nil {
				return err
			}
			continue
		}
		if isLikelyNonMediaResponse(resp) {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("分片响应不是媒体内容: %s", resp.Header.Get("Content-Type"))
			urlIdx = (urlIdx + 1) % len(urls)
			if err := waitForRetry(ctx, 300*time.Millisecond); err != nil {
				return err
			}
			continue
		}

		rStart, rEnd, rTotal, rangeErr := parseContentRange(resp.Header.Get("Content-Range"))
		validRange := rangeErr == nil && rStart == requestStart && rEnd >= requestStart && rEnd <= segment.end && rTotal == s.totalSize
		declaredLength := rEnd - rStart + 1
		if validRange && resp.ContentLength >= 0 && resp.ContentLength != declaredLength {
			validRange = false
		}
		if !validRange {
			_ = resp.Body.Close()
			urlIdx = (urlIdx + 1) % len(urls)
			if err := waitForRetry(ctx, 300*time.Millisecond); err != nil {
				return err
			}
			continue
		}

		part, err := os.OpenFile(segment.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			_ = resp.Body.Close()
			return err
		}
		written, copyErr := copyExact(resp.Body, part, declaredLength)
		closeErr := part.Close()
		bodyCloseErr := resp.Body.Close()
		if closeErr != nil && copyErr == nil {
			copyErr = closeErr
		}
		if bodyCloseErr != nil && copyErr == nil {
			copyErr = bodyCloseErr
		}
		if written > 0 && progressFn != nil {
			progressFn(written)
		}
		if copyErr != nil || written != declaredLength {
			urlIdx = (urlIdx + 1) % len(urls)
			if err := waitForRetry(ctx, 300*time.Millisecond); err != nil {
				return err
			}
			continue
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("分片下载重试次数耗尽")
}

func filterStreamURLs(urls []string) []string {
	filtered := make([]string, 0, len(urls))
	for _, u := range urls {
		if strings.TrimSpace(u) != "" {
			filtered = append(filtered, u)
		}
	}
	return filtered
}

func chooseRefreshedURLIndex(urls []string, failedURL string) int {
	if len(urls) > 1 && urls[0] == failedURL {
		for i := 1; i < len(urls); i++ {
			if urls[i] != failedURL {
				return i
			}
		}
	}
	return 0
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

func assembleSegmentParts(ctx context.Context, targetPath string, segments []streamSegment) error {
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, ".bbdown-assemble-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	complete := false
	defer func() {
		_ = tmp.Close()
		if !complete {
			_ = os.Remove(tmpPath)
		}
	}()
	buf := make([]byte, 1024*1024)
	for _, segment := range segments {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := os.Stat(segment.path)
		if err != nil {
			return fmt.Errorf("读取并发分片失败: %w", err)
		}
		expectedSize := segment.end - segment.start + 1
		if info.Size() != expectedSize {
			return fmt.Errorf("并发分片不完整: %s (实际 %d / 预期 %d)", segment.path, info.Size(), expectedSize)
		}
		part, err := os.Open(segment.path)
		if err != nil {
			return err
		}
		if _, err := copyWithContext(ctx, tmp, part, buf); err != nil {
			_ = part.Close()
			return err
		}
		if err := part.Close(); err != nil {
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := copyOrRenameContext(ctx, tmpPath, targetPath); err != nil {
		return err
	}
	complete = true
	return nil
}

func removeSegmentPartFiles(targetPath string, count int) {
	for i := 0; i < count; i++ {
		_ = os.Remove(streamSegmentPath(targetPath, i))
	}
}

// DownloadSingleStream 核心无损连续追加断点续传（保证数据 0 空洞、0 损坏、100% 完整性）
func (s *StreamDownloader) DownloadSingleStream(ctx context.Context, progressFn DownloadProgressFn) error {
	if strings.TrimSpace(s.targetPath) == "" {
		return fmt.Errorf("目标文件路径为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(filepath.Dir(s.targetPath), 0755); err != nil {
		return fmt.Errorf("创建下载目录失败: %w", err)
	}

	// 1. 探测流的预期总大小
	if s.totalSize <= 0 {
		_, _ = s.GetTotalSize(ctx)
	}

	// 2. 检查本地已下载的文件状态
	if info, err := os.Stat(s.targetPath); err == nil {
		actualSize := info.Size()
		// 如果本地已有文件且大小严格等于预期大小，说明已完整
		if s.totalSize > 0 && actualSize == s.totalSize {
			return nil
		}
		// 如果本地文件大于预期大小（异常脏数据），清理并从头下载
		if s.totalSize > 0 && actualSize > s.totalSize {
			if err := os.Remove(s.targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("清理过大的临时文件失败: %w", err)
			}
			if progressFn != nil && actualSize > 0 {
				progressFn(-actualSize)
			}
		}
	}

	const maxRetries = 15
	var lastErr error
	var lastProgressOffset int64 = -1

	for attempt := 0; attempt < maxRetries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		var startOffset int64 = 0
		if info, err := os.Stat(s.targetPath); err == nil {
			startOffset = info.Size()
		}

		if s.totalSize > 0 && startOffset == s.totalSize {
			return nil
		}
		if s.totalSize > 0 && startOffset > s.totalSize {
			// 文件过长，存在脏数据，彻底清理并扣减进度统计
			if err := os.Remove(s.targetPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("清理过大的临时文件失败: %w", err)
			}
			if progressFn != nil && startOffset > 0 {
				progressFn(-startOffset)
			}
			startOffset = 0
		}

		currentURL := s.currentURL()
		if currentURL == "" {
			return fmt.Errorf("无有效下载 URL")
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, currentURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", bilibili.BrowserUA)
		req.Header.Set("Referer", bilibili.Referer)
		if startOffset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", startOffset))
		}

		// 只要有任何真实的下载进度推进，就重置尝试次数，避免长时间下载积累的非连续网络闪断耗尽重试次数
		if startOffset > lastProgressOffset && lastProgressOffset != -1 {
			attempt = 0
		}
		lastProgressOffset = startOffset

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.rotateURL()
			if err := waitForRetry(ctx, 300*time.Millisecond); err != nil {
				return err
			}
			continue
		}

		// 处理 416 边界情况 (已在文件末尾或 Range 超限)
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			cr := resp.Header.Get("Content-Range")
			resp.Body.Close()

			// 1. 尝试从 416 响应头提取服务端宣告的真实总大小
			if cr != "" {
				_, _, rTotal, pErr := parseContentRange(cr)
				if pErr == nil && rTotal > 0 && s.totalSize <= 0 {
					s.totalSize = rTotal
				}
			}

			// 2. 若本地文件大小严格等于预期大小，说明已完整下载
			if info, err := os.Stat(s.targetPath); err == nil && s.totalSize > 0 && info.Size() == s.totalSize {
				return nil
			}

			// 3. 本地分块超长或在未知大小下触发 416（证明现有分块已超出服务端范围或已损坏）：主动清理脏数据并重置从头下载
			if fi, err := os.Stat(s.targetPath); err == nil {
				if fi.Size() > 0 {
					removedBytes := fi.Size()
					if err := os.Remove(s.targetPath); err != nil && !os.IsNotExist(err) {
						return fmt.Errorf("清理 416 脏文件失败: %w", err)
					}
					if progressFn != nil && removedBytes > 0 {
						progressFn(-removedBytes)
					}
				}
			}
			s.rotateURL()
			if err := waitForRetry(ctx, 200*time.Millisecond); err != nil {
				return err
			}
			continue
		}

		// 处理 403 Forbidden (直链鉴权过期)
		if resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			lastErr = fmt.Errorf("URL auth expired (403)")
			// 若配置了 URL 动态刷新器，立即无缝拉取最新可用直链并重试断点续传
			if s.refresher != nil {
				if newURLs, rErr := s.refresher(ctx); rErr == nil && len(newURLs) > 0 {
					// 刷新结果可能仍把刚刚返回 403 的 CDN 排在第一位。
					// 此时必须优先尝试列表中的其他节点，否则备用 CDN 永远不会被使用。
					failedURL := currentURL
					filteredURLs := make([]string, 0, len(newURLs))
					for _, u := range newURLs {
						if strings.TrimSpace(u) != "" {
							filteredURLs = append(filteredURLs, u)
						}
					}
					if len(filteredURLs) == 0 {
						continue
					}

					nextIdx := 0
					if filteredURLs[0] == failedURL {
						for i := 1; i < len(filteredURLs); i++ {
							if filteredURLs[i] != failedURL {
								nextIdx = i
								break
							}
						}
					}

					s.urls = filteredURLs
					s.urlIdx = nextIdx

					// 只有切换到了不同地址才重置 attempt；刷新后仍是同一地址时
					// 保留原有上限，防止服务端持续 403 造成无限循环。
					if s.urls[s.urlIdx] != failedURL {
						attempt = 0
					}

					if err := waitForRetry(ctx, 100*time.Millisecond); err != nil {
						return err
					}
					continue
				}
			}
			lastErr = fmt.Errorf("CDN 节点响应异常: %s", resp.Status)
			s.rotateURL()
			if err := waitForRetry(ctx, 500*time.Millisecond); err != nil {
				return err
			}
			continue
		}

		// 处理 5xx 服务端异常
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("CDN 节点响应异常: %s", resp.Status)
			s.rotateURL()
			if err := waitForRetry(ctx, 500*time.Millisecond); err != nil {
				return err
			}
			continue
		}

		// 处理非 200/206 状态码
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP 状态码异常: %s", resp.Status)
			s.rotateURL()
			if err := waitForRetry(ctx, 500*time.Millisecond); err != nil {
				return err
			}
			continue
		}
		if isLikelyNonMediaResponse(resp) {
			resp.Body.Close()
			lastErr = fmt.Errorf("媒体响应类型异常: %s", resp.Header.Get("Content-Type"))
			s.rotateURL()
			if err := waitForRetry(ctx, 300*time.Millisecond); err != nil {
				return err
			}
			continue
		}

		// 检查服务器响应类型与 Content-Range 精准校验
		var isAppend bool
		if resp.StatusCode == http.StatusPartialContent {
			cr := strings.TrimSpace(resp.Header.Get("Content-Range"))
			rStart, rEnd, rTotal, pErr := parseContentRange(cr)
			invalidRange := pErr != nil || rStart < 0 || rEnd < rStart || rTotal <= 0
			if !invalidRange && startOffset > 0 && rStart != startOffset {
				invalidRange = true
			}
			if !invalidRange && startOffset == 0 && rStart != 0 {
				invalidRange = true
			}
			if !invalidRange && s.totalSize > 0 && rTotal > 0 && rTotal != s.totalSize {
				invalidRange = true
			}

			if invalidRange {
				resp.Body.Close()
				if pErr != nil {
					lastErr = fmt.Errorf("Content-Range 无效 (%q)，重置为从头下载: %w", cr, pErr)
				} else if rStart != startOffset {
					lastErr = fmt.Errorf("Content-Range 起点错位 (请求 %d, 响应 %d)，重置为从头下载", startOffset, rStart)
				} else {
					lastErr = fmt.Errorf("Content-Range 总长度冲突 (本地预期 %d, 响应 %d)，重置为从头下载", s.totalSize, rTotal)
				}
				var removedBytes int64
				if fi, err := os.Stat(s.targetPath); err == nil {
					removedBytes = fi.Size()
				}
				removeErr := os.Remove(s.targetPath)
				if removeErr != nil && !os.IsNotExist(removeErr) {
					return fmt.Errorf("清理 Content-Range 错位文件失败: %w", removeErr)
				}
				if removeErr == nil && progressFn != nil && removedBytes > 0 {
					progressFn(-removedBytes)
				}
				s.rotateURL()
				if err := waitForRetry(ctx, 300*time.Millisecond); err != nil {
					return err
				}
				continue
			}

			if rTotal > 0 && s.totalSize <= 0 {
				s.totalSize = rTotal
			}
			if s.totalSize <= 0 && resp.ContentLength > 0 {
				// 200/206 的 Content-Length 只有在表示当前实体长度时才可用于
				// 完整性校验；206 的总长度未知时不能把分片长度当作文件总长。
				if resp.StatusCode == http.StatusOK {
					s.totalSize = resp.ContentLength
				}
			}
			// 只有已有本地前缀时才允许追加；从头开始的 206 仍必须覆盖写入。
			isAppend = startOffset > 0
		} else {
			// 服务器返回 200 OK（说明服务器不支持 Range 或从 0 开始下载），必须从头重写，不能追加！
			isAppend = false
			// 200 响应的 Content-Length 表示完整实体长度，可以作为最终完整性校验依据。
			if s.totalSize <= 0 && resp.ContentLength > 0 {
				s.totalSize = resp.ContentLength
			}
			// 扣除之前已计入的字节进度，消除从头重写导致的进度统计漂移
			if startOffset > 0 && progressFn != nil {
				progressFn(-startOffset)
			}
		}

		downloadErr := s.streamToFile(ctx, resp.Body, isAppend, progressFn)
		resp.Body.Close()

		if downloadErr == nil {
			// 校验最终下载文件的大小是否精准吻合
			info, statErr := os.Stat(s.targetPath)
			if statErr != nil {
				return fmt.Errorf("下载完成后无法校验临时文件: %w", statErr)
			}
			if s.totalSize > 0 && info.Size() != s.totalSize {
				if info.Size() > s.totalSize {
					removeErr := os.Remove(s.targetPath)
					if removeErr != nil && !os.IsNotExist(removeErr) {
						return fmt.Errorf("清理超长下载文件失败: %w", removeErr)
					}
					if removeErr == nil && progressFn != nil {
						progressFn(-info.Size())
					}
				}
				lastErr = fmt.Errorf("数据流未完整 (实际 %d 字节 / 预期 %d 字节)，自动重试", info.Size(), s.totalSize)
				s.rotateURL()
				if err := waitForRetry(ctx, 300*time.Millisecond); err != nil {
					return err
				}
				continue
			}
			if s.totalSize <= 0 {
				// 没有 Content-Length 或完整 Content-Range 时，EOF 只能说明
				// 当前连接结束，不能证明媒体实体已经完整。宁可让任务可重试，
				// 也不能把未知长度的截断文件报告为成功。
				lastErr = fmt.Errorf("下载响应未提供可验证的总长度，拒绝将 EOF 视为完成")
				if len(s.urls) > 1 {
					s.rotateURL()
					if err := waitForRetry(ctx, 300*time.Millisecond); err != nil {
						return err
					}
					continue
				}
				return lastErr
			}
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		lastErr = downloadErr
		s.rotateURL()
		if err := waitForRetry(ctx, 300*time.Millisecond); err != nil {
			return err
		}
	}

	return lastErr
}

type readResult struct {
	data []byte
	err  error
}

func (s *StreamDownloader) streamToFile(ctx context.Context, body io.Reader, isAppend bool, progressFn DownloadProgressFn) error {
	var flag int
	if isAppend {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	} else {
		flag = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}

	file, err := os.OpenFile(s.targetPath, flag, 0644)
	if err != nil {
		return fmt.Errorf("打开分块临时文件失败: %w", err)
	}
	defer file.Close()

	readChan := make(chan readResult, 4)
	readCtx, cancelRead := context.WithCancel(ctx)
	bodyCloser, _ := body.(io.Closer)
	defer func() {
		cancelRead()
		if bodyCloser != nil {
			_ = bodyCloser.Close()
		}
	}()

	go func() {
		tempBuf := make([]byte, 128*1024) // 128KB 高性能传输缓冲
		for {
			select {
			case <-readCtx.Done():
				return
			default:
			}

			n, rErr := body.Read(tempBuf)
			if n == 0 && rErr == nil {
				rErr = io.ErrNoProgress
			}
			var data []byte
			if n > 0 {
				data = make([]byte, n)
				copy(data, tempBuf[:n])
			}

			select {
			case readChan <- readResult{data: data, err: rErr}:
			case <-readCtx.Done():
				return
			}

			if rErr != nil {
				return
			}
		}
	}()

	const readIdleTimeout = 8 * time.Second
	idleTimer := time.NewTimer(readIdleTimeout)
	defer idleTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case res := <-readChan:
			if len(res.data) > 0 {
				if wErr := writeAll(file, res.data); wErr != nil {
					return fmt.Errorf("写入临时分块失败: %w", wErr)
				}
				if progressFn != nil {
					progressFn(int64(len(res.data)))
				}
			}
			if res.err != nil {
				if res.err == io.EOF {
					return nil
				}
				return res.err
			}
			// 成功读取到有效数据，复用并重置空闲超时定时器
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(readIdleTimeout)
		case <-idleTimer.C:
			return fmt.Errorf("CDN数据流读取停滞(超过8秒无数据)，自动重连加速")
		}
	}
}
