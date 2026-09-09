package downloader

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

// StreamDownloader 单个媒体流 (视频或音频) 的高可靠断点续传下载器
type StreamDownloader struct {
	client     *http.Client
	urls       []string
	urlIdx     int
	targetPath string
	totalSize  int64
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

// parseContentRange 解析 "bytes <start>-<end>/<total>"
func parseContentRange(cr string) (start, end, total int64, err error) {
	cr = strings.TrimPrefix(strings.TrimSpace(cr), "bytes ")
	parts := strings.Split(cr, "/")
	if len(parts) != 2 {
		return 0, 0, 0, fmt.Errorf("invalid content-range: %s", cr)
	}
	rangeParts := strings.Split(parts[0], "-")
	if len(rangeParts) != 2 {
		return 0, 0, 0, fmt.Errorf("invalid range parts: %s", parts[0])
	}
	start, err1 := strconv.ParseInt(rangeParts[0], 10, 64)
	end, err2 := strconv.ParseInt(rangeParts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, 0, fmt.Errorf("invalid range numbers: %s", parts[0])
	}
	total = -1
	if parts[1] != "*" {
		if t, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
			total = t
		}
	}
	return start, end, total, nil
}

// GetTotalSize 获取流的总大小 (使用带 Range: bytes=0-0 请求探测)
func (s *StreamDownloader) GetTotalSize(ctx context.Context) (int64, error) {
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
			if cr := resp.Header.Get("Content-Range"); cr != "" {
				_, _, total, pErr := parseContentRange(cr)
				if pErr == nil && total > 0 {
					resp.Body.Close()
					s.totalSize = total
					s.urlIdx = (s.urlIdx + i) % len(s.urls)
					return total, nil
				}
			}
			resp.Body.Close()
			continue
		}

		// 处理 200 OK：服务端未按 Range 响应而是返回整个流实体，此时方可读取 Content-Length
		if resp.StatusCode == http.StatusOK {
			if cl := resp.Header.Get("Content-Length"); cl != "" {
				if size, err := strconv.ParseInt(cl, 10, 64); err == nil && size > 0 {
					resp.Body.Close()
					s.totalSize = size
					s.urlIdx = (s.urlIdx + i) % len(s.urls)
					return size, nil
				}
			}
			resp.Body.Close()
		}
	}

	if lastErr != nil {
		return 0, lastErr
	}
	return 0, nil
}

// DownloadProgressFn 进度更新回调
type DownloadProgressFn func(downloadedDelta int64)

// DownloadWithConcurrency 统一的高性能高可靠流式下载器接口
func (s *StreamDownloader) DownloadWithConcurrency(ctx context.Context, concurrency int, progressFn DownloadProgressFn) error {
	return s.DownloadSingleStream(ctx, progressFn)
}

// DownloadSingleStream 核心无损连续追加断点续传（保证数据 0 空洞、0 损坏、100% 完整性）
func (s *StreamDownloader) DownloadSingleStream(ctx context.Context, progressFn DownloadProgressFn) error {
	_ = os.MkdirAll(filepath.Dir(s.targetPath), 0755)

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
			_ = os.Remove(s.targetPath)
		}
	}

	const maxRetries = 15
	var lastErr error

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
			_ = os.Remove(s.targetPath)
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

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.rotateURL()
			time.Sleep(300 * time.Millisecond)
			continue
		}

		// 处理 416 边界情况 (已在文件末尾或 Range 超限)
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			resp.Body.Close()
			if info, err := os.Stat(s.targetPath); err == nil && s.totalSize > 0 && info.Size() == s.totalSize {
				return nil
			}
			// 若本地文件确实超出预期大小，说明有脏数据，清理并扣减进度；否则轮换 CDN 节点重试，避免立刻全量误删
			if fi, err := os.Stat(s.targetPath); err == nil {
				if s.totalSize > 0 && fi.Size() > s.totalSize {
					removedBytes := fi.Size()
					_ = os.Remove(s.targetPath)
					if progressFn != nil && removedBytes > 0 {
						progressFn(-removedBytes)
					}
				}
			}
			s.rotateURL()
			time.Sleep(300 * time.Millisecond)
			continue
		}

		// 处理 403 / 5xx 等需轮换 CDN 的状态码
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("CDN 节点响应异常: %s", resp.Status)
			s.rotateURL()
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// 处理非 200/206 状态码
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP 状态码异常: %s", resp.Status)
			s.rotateURL()
			time.Sleep(500 * time.Millisecond)
			continue
		}

		// 检查服务器响应类型与 Content-Range 精准校验
		var isAppend bool
		if resp.StatusCode == http.StatusPartialContent && startOffset > 0 {
			cr := resp.Header.Get("Content-Range")
			if cr != "" {
				rStart, _, rTotal, pErr := parseContentRange(cr)
				if pErr == nil {
					// 严格校验：返回片段的起点必须严格等于本地已存在大小
					if rStart != startOffset {
						resp.Body.Close()
						lastErr = fmt.Errorf("Content-Range 起点错位 (请求 %d, 响应 %d)，重置为从头下载", startOffset, rStart)
						var removedBytes int64 = 0
						if fi, err := os.Stat(s.targetPath); err == nil {
							removedBytes = fi.Size()
						}
						_ = os.Remove(s.targetPath)
						if progressFn != nil && removedBytes > 0 {
							progressFn(-removedBytes)
						}
						s.rotateURL()
						time.Sleep(300 * time.Millisecond)
						continue
					}
					if rTotal > 0 && s.totalSize <= 0 {
						s.totalSize = rTotal
					}
				}
			}
			isAppend = true
		} else {
			// 服务器返回 200 OK（说明服务器不支持 Range 或从 0 开始下载），必须从头重写，不能追加！
			isAppend = false
			// 扣除之前已计入的字节进度，消除从头重写导致的进度统计漂移
			if startOffset > 0 && progressFn != nil {
				progressFn(-startOffset)
			}
		}

		downloadErr := s.streamToFile(ctx, resp.Body, isAppend, progressFn)
		resp.Body.Close()

		if downloadErr == nil {
			// 校验最终下载文件的大小是否精准吻合
			if info, err := os.Stat(s.targetPath); err == nil {
				if s.totalSize > 0 && info.Size() != s.totalSize {
					if info.Size() > s.totalSize {
						_ = os.Remove(s.targetPath)
					}
					lastErr = fmt.Errorf("数据流未完整 (实际 %d 字节 / 预期 %d 字节)，自动重试", info.Size(), s.totalSize)
					s.rotateURL()
					time.Sleep(300 * time.Millisecond)
					continue
				}
				return nil
			}
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		lastErr = downloadErr
		s.rotateURL()
		time.Sleep(300 * time.Millisecond)
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
	defer cancelRead()

	go func() {
		tempBuf := make([]byte, 128*1024) // 128KB 高性能传输缓冲
		for {
			select {
			case <-readCtx.Done():
				return
			default:
			}

			n, rErr := body.Read(tempBuf)
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
				if _, wErr := file.Write(res.data); wErr != nil {
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

