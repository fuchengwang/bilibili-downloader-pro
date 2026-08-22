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
	"sync"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

// StreamDownloader 单个媒体流 (视频或音频) 的断点续传、多线程分片与防假死自动降级下载器
type StreamDownloader struct {
	client     *http.Client
	url        string
	targetPath string
	totalSize  int64
}

// NewStreamDownloader 创建单流下载器 (使用长连接 Transport，无单次请求总耗时限制)
func NewStreamDownloader(streamURL string, targetPath string) *StreamDownloader {
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
			Timeout:   0, // 大文件下载必须为 0，避免流读取因固定超时被 context deadline 取消
		},
		url:        streamURL,
		targetPath: targetPath,
	}
}

// GetTotalSize 获取流的总大小 (使用带 Range: bytes=0-0 请求探测)
func (s *StreamDownloader) GetTotalSize(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", bilibili.BrowserUA)
	req.Header.Set("Referer", bilibili.Referer)
	req.Header.Set("Range", "bytes=0-0")

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if cr := resp.Header.Get("Content-Range"); cr != "" {
		// Content-Range: bytes 0-0/1234567
		if idx := len(cr) - 1; idx > 0 {
			for i := len(cr) - 1; i >= 0; i-- {
				if cr[i] == '/' {
					totalStr := cr[i+1:]
					if size, err := strconv.ParseInt(totalStr, 10, 64); err == nil {
						s.totalSize = size
						return size, nil
					}
					break
				}
			}
		}
	}

	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if size, err := strconv.ParseInt(cl, 10, 64); err == nil {
			s.totalSize = size
			return size, nil
		}
	}

	return 0, nil
}

// DownloadProgressFn 进度更新回调
type DownloadProgressFn func(downloadedDelta int64)

// DownloadWithConcurrency 支持多线程分片并发下载，并带自动平滑回退单线程保护机制
func (s *StreamDownloader) DownloadWithConcurrency(ctx context.Context, concurrency int, progressFn DownloadProgressFn) error {
	// 如果设置线程数为 1，或文件较小（<5MB），或未知文件大小，直接使用最平稳的单流下载
	if concurrency <= 1 || s.totalSize < 5*1024*1024 {
		return s.DownloadSingleStream(ctx, progressFn)
	}

	if concurrency > 8 {
		concurrency = 8
	}

	// 尝试多线程分片下载
	err := s.downloadMultiThread(ctx, concurrency, progressFn)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 🌟 核心稳定性兜底：若多线程出现任何 CDN 异常或分片错误，立即平滑降级回退到单线程下载
		return s.DownloadSingleStream(ctx, progressFn)
	}

	return nil
}

// downloadMultiThread 执行多线程 Range 分片并发下载
func (s *StreamDownloader) downloadMultiThread(ctx context.Context, concurrency int, progressFn DownloadProgressFn) error {
	_ = os.MkdirAll(filepath.Dir(s.targetPath), 0755)

	// 如果文件已经存在且已完成
	if fi, err := os.Stat(s.targetPath); err == nil && s.totalSize > 0 && fi.Size() >= s.totalSize {
		return nil
	}

	file, err := os.OpenFile(s.targetPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("创建输出文件失败: %w", err)
	}
	defer file.Close()

	// 预先设定文件大小
	_ = file.Truncate(s.totalSize)

	chunkSize := s.totalSize / int64(concurrency)
	var wg sync.WaitGroup
	errChan := make(chan error, concurrency)
	subCtx, cancelSub := context.WithCancel(ctx)
	defer cancelSub()

	for i := 0; i < concurrency; i++ {
		start := int64(i) * chunkSize
		end := start + chunkSize - 1
		if i == concurrency-1 {
			end = s.totalSize - 1
		}

		wg.Add(1)
		go func(workerIdx int, cStart, cEnd int64) {
			defer wg.Done()
			wErr := s.downloadChunk(subCtx, file, workerIdx, cStart, cEnd, progressFn)
			if wErr != nil {
				select {
				case errChan <- wErr:
					cancelSub() // 发生错误时取消其他并发线程
				default:
				}
			}
		}(i, start, end)
	}

	wg.Wait()
	close(errChan)

	if len(errChan) > 0 {
		return <-errChan
	}

	return nil
}

// downloadChunk 单个分片的下载与写入
func (s *StreamDownloader) downloadChunk(ctx context.Context, file *os.File, workerIdx int, start, end int64, progressFn DownloadProgressFn) error {
	const maxRetries = 5
	var curOffset = start

	for attempt := 0; attempt < maxRetries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if curOffset > end {
			return nil
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", bilibili.BrowserUA)
		req.Header.Set("Referer", bilibili.Referer)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", curOffset, end))

		resp, err := s.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(300 * time.Millisecond)
			continue
		}

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			time.Sleep(300 * time.Millisecond)
			continue
		}

		// 使用流式写入带卡死检测
		writeErr := s.streamToChunkFile(ctx, file, curOffset, resp.Body, func(delta int64) {
			curOffset += delta
			if progressFn != nil {
				progressFn(delta)
			}
		})
		resp.Body.Close()

		if writeErr == nil {
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		time.Sleep(200 * time.Millisecond)
	}

	if curOffset <= end {
		return fmt.Errorf("分片 %d 下载未完整", workerIdx)
	}
	return nil
}

func (s *StreamDownloader) streamToChunkFile(ctx context.Context, file *os.File, startOffset int64, body io.Reader, progressFn DownloadProgressFn) error {
	readChan := make(chan readResult, 4)
	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()

	go func() {
		tempBuf := make([]byte, 64*1024)
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

	const readIdleTimeout = 6 * time.Second
	var writeOffset = startOffset

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case res := <-readChan:
			if len(res.data) > 0 {
				if _, wErr := file.WriteAt(res.data, writeOffset); wErr != nil {
					return fmt.Errorf("写入分片文件失败: %w", wErr)
				}
				writeOffset += int64(len(res.data))
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
		case <-time.After(readIdleTimeout):
			return fmt.Errorf("CDN分片读取停滞(超过6秒无数据)")
		}
	}
}

// DownloadSingleStream 执行平稳的单流断点续传下载
func (s *StreamDownloader) DownloadSingleStream(ctx context.Context, progressFn DownloadProgressFn) error {
	_ = os.MkdirAll(filepath.Dir(s.targetPath), 0755)

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

		if s.totalSize > 0 && startOffset >= s.totalSize {
			return nil
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
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
			time.Sleep(300 * time.Millisecond)
			continue
		}

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
				return nil
			}
			lastErr = fmt.Errorf("HTTP 状态码异常: %s", resp.Status)
			time.Sleep(500 * time.Millisecond)
			continue
		}

		downloadErr := s.streamToFile(ctx, resp.Body, progressFn)
		resp.Body.Close()

		if downloadErr == nil {
			return nil
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}

		lastErr = downloadErr
		if strings.Contains(downloadErr.Error(), "停滞") {
			time.Sleep(100 * time.Millisecond)
		} else {
			time.Sleep(500 * time.Millisecond)
		}
	}

	return lastErr
}

type readResult struct {
	data []byte
	err  error
}

func (s *StreamDownloader) streamToFile(ctx context.Context, body io.Reader, progressFn DownloadProgressFn) error {
	file, err := os.OpenFile(s.targetPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("打开分块临时文件失败: %w", err)
	}
	defer file.Close()

	readChan := make(chan readResult, 4)
	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()

	go func() {
		tempBuf := make([]byte, 64*1024)
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

	const readIdleTimeout = 6 * time.Second

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
		case <-time.After(readIdleTimeout):
			return fmt.Errorf("CDN数据流读取停滞(超过6秒无数据)，自动重连加速")
		}
	}
}
