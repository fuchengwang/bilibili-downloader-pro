package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type streamRoundTripperFunc func(*http.Request) (*http.Response, error)

func (fn streamRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type closeTrackingBody struct {
	io.Reader
	closeCount int32
}

func (b *closeTrackingBody) Close() error {
	atomic.AddInt32(&b.closeCount, 1)
	return nil
}

type noProgressReader struct{}

func (noProgressReader) Read([]byte) (int, error) { return 0, nil }

func TestStreamDownloader_RejectsHTMLAsMediaResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", "18")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "<html>login</html>")
	}))
	defer ts.Close()

	target := filepath.Join(t.TempDir(), "not-media.mp4")
	dl := NewStreamDownloader([]string{ts.URL}, target)
	err := dl.DownloadSingleStream(context.Background(), nil)
	if err == nil || (!strings.Contains(err.Error(), "不是媒体内容") && !strings.Contains(err.Error(), "媒体响应类型异常")) {
		t.Fatalf("HTML 响应不应被当作媒体成功下载，err=%v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("HTML 响应失败后不应留下成品文件，stat=%v", statErr)
	}
}

func TestStreamDownloader_StreamToFileRejectsNoProgressReader(t *testing.T) {
	dl := NewStreamDownloader([]string{"https://cdn.invalid/media"}, filepath.Join(t.TempDir(), "no-progress"))
	err := dl.streamToFile(context.Background(), noProgressReader{}, false, nil)
	if !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("无进展 Reader 应立即失败，err=%v", err)
	}
}

func TestStreamDownloader_GetTotalSizeClosesErrorBodyAndReturnsStatus(t *testing.T) {
	body := &closeTrackingBody{Reader: strings.NewReader("forbidden")}
	dl := NewStreamDownloader([]string{"https://cdn.invalid/media"}, filepath.Join(t.TempDir(), "media"))
	dl.client = &http.Client{Transport: streamRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Status:     "403 Forbidden",
			Header:     make(http.Header),
			Body:       body,
			Request:    req,
		}, nil
	})}

	size, err := dl.GetTotalSize(context.Background())
	if err == nil || !strings.Contains(err.Error(), "403 Forbidden") {
		t.Fatalf("探测 403 应返回包含 HTTP 状态的错误，size=%d err=%v", size, err)
	}
	if got := atomic.LoadInt32(&body.closeCount); got != 1 {
		t.Fatalf("探测失败响应体应关闭一次，实际关闭 %d 次", got)
	}
}

func TestStreamDownloader_GetTotalSizeRejectsUnknownOKLength(t *testing.T) {
	body := &closeTrackingBody{Reader: strings.NewReader("unknown")}
	dl := NewStreamDownloader([]string{"https://cdn.invalid/media"}, filepath.Join(t.TempDir(), "media"))
	dl.client = &http.Client{Transport: streamRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        make(http.Header),
			Body:          body,
			ContentLength: -1,
			Request:       req,
		}, nil
	})}

	size, err := dl.GetTotalSize(context.Background())
	if size != 0 || err == nil || !strings.Contains(err.Error(), "Content-Length") {
		t.Fatalf("未知长度的 200 探测应失败，size=%d err=%v", size, err)
	}
	if got := atomic.LoadInt32(&body.closeCount); got != 1 {
		t.Fatalf("未知长度响应体应关闭一次，实际关闭 %d 次", got)
	}
}

// TestStreamDownloader_ResumeAndIntegrity 测试断点续传的无损完整性与零空洞
func TestStreamDownloader_ResumeAndIntegrity(t *testing.T) {
	dataSize := 2 * 1024 * 1024
	srcData := make([]byte, dataSize)
	r := rand.New(rand.NewSource(42))
	r.Read(srcData)
	expectedHash := sha256.Sum256(srcData)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rangeHdr := req.Header.Get("Range")
		if rangeHdr == "" {
			w.Header().Set("Content-Length", strconv.Itoa(dataSize))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(srcData)
			return
		}

		if !strings.HasPrefix(rangeHdr, "bytes=") {
			http.Error(w, "invalid range", http.StatusBadRequest)
			return
		}

		rangeSpec := strings.TrimPrefix(rangeHdr, "bytes=")
		parts := strings.Split(rangeSpec, "-")
		start, _ := strconv.ParseInt(parts[0], 10, 64)
		end := int64(dataSize - 1)
		if len(parts) > 1 && parts[1] != "" {
			end, _ = strconv.ParseInt(parts[1], 10, 64)
		}

		if start > int64(dataSize-1) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", dataSize))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}

		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, dataSize))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(srcData[start : end+1])
	}))
	defer ts.Close()

	tmpDir, err := os.MkdirTemp("", "stream_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	targetFile := filepath.Join(tmpDir, "test_video.downloading")

	// 阶段 1：模拟下载了一半被用户点击“暂停”
	halfSize := dataSize / 2
	if err := os.WriteFile(targetFile, srcData[:halfSize], 0644); err != nil {
		t.Fatal(err)
	}

	dl := NewStreamDownloader([]string{ts.URL}, targetFile)

	var deltaSum int64
	// 阶段 2：模拟用户点击“继续/恢复下载”
	err = dl.DownloadWithConcurrency(context.Background(), 4, func(delta int64) {
		deltaSum += delta
	})
	if err != nil {
		t.Fatalf("断点续传失败: %v", err)
	}

	// 阶段 3：校验最终文件
	finalData, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("读取最终文件失败: %v", err)
	}

	if len(finalData) != dataSize {
		t.Fatalf("文件大小不匹配: 期望 %d, 实际 %d", dataSize, len(finalData))
	}

	actualHash := sha256.Sum256(finalData)
	if !bytes.Equal(expectedHash[:], actualHash[:]) {
		t.Fatalf("断点续传合成后的哈希校验不一致！期望 %x, 实际 %x", expectedHash, actualHash)
	}
}

// TestStreamDownloader_ContentRangeMismatchRecovery 测试 Content-Range 起点错位时自动清空并从头重下
func TestStreamDownloader_ContentRangeMismatchRecovery(t *testing.T) {
	dataSize := 8
	srcData := make([]byte, dataSize)
	for i := range srcData {
		srcData[i] = byte('A' + i)
	}

	var requestCount int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		rangeHdr := req.Header.Get("Range")

		// 探测请求返回正常总长度。
		if rangeHdr == "bytes=0-0" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", dataSize))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(srcData[:1])
			return
		}

		// 恢复请求故意返回错误起点，但返回长度刚好让旧逻辑误以为下载完成。
		if count == 2 && rangeHdr == "bytes=4-" {
			w.Header().Set("Content-Range", "bytes 0-3/8")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(srcData[:4])
			return
		}

		w.Header().Set("Content-Length", strconv.Itoa(dataSize))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(srcData)
	}))
	defer ts.Close()

	tmpDir, _ := os.MkdirTemp("", "range_mismatch_*")
	defer os.RemoveAll(tmpDir)

	targetFile := filepath.Join(tmpDir, "mismatch.downloading")
	// 预置正确的前缀，模拟暂停后的断点。
	_ = os.WriteFile(targetFile, srcData[:4], 0644)

	dl := NewStreamDownloader([]string{ts.URL}, targetFile)
	err := dl.DownloadSingleStream(context.Background(), nil)
	if err != nil {
		t.Fatalf("错位恢复下载失败: %v", err)
	}

	finalData, _ := os.ReadFile(targetFile)
	if !bytes.Equal(finalData, srcData) {
		t.Fatalf("错位恢复后文件内容错误: got %q, want %q", finalData, srcData)
	}
}

func TestStreamDownloader_ResumeRejectsMissingContentRange(t *testing.T) {
	srcData := []byte("ABCDEFGH")
	var requestCount int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		rangeHdr := req.Header.Get("Range")
		if rangeHdr == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/8")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(srcData[:1])
			return
		}
		if count == 2 && rangeHdr == "bytes=4-" {
			// 缺少 Content-Range 时，不能把响应直接追加到本地前缀。
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(srcData[:4])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(srcData)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(srcData)
	}))
	defer ts.Close()

	targetFile := filepath.Join(t.TempDir(), "missing-range.downloading")
	if err := os.WriteFile(targetFile, srcData[:4], 0644); err != nil {
		t.Fatal(err)
	}

	dl := NewStreamDownloader([]string{ts.URL}, targetFile)
	if err := dl.DownloadSingleStream(context.Background(), nil); err != nil {
		t.Fatalf("缺失 Content-Range 后恢复下载失败: %v", err)
	}
	actual, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, srcData) {
		t.Fatalf("缺失 Content-Range 后文件内容错误: got %q, want %q", actual, srcData)
	}
}

func TestStreamDownloader_ResumeRejectsUnknownContentRangeTotal(t *testing.T) {
	srcData := []byte("ABCDEFGH")
	var requestCount int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		if req.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", "bytes 0-0/8")
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(srcData[:1])
			return
		}
		if count == 2 {
			// 206 的总长度未知时不能验证这是同一个媒体实体。
			w.Header().Set("Content-Range", "bytes 4-7/*")
			w.Header().Set("Content-Length", "4")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("XXXX"))
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(srcData)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(srcData)
	}))
	defer ts.Close()

	target := filepath.Join(t.TempDir(), "unknown-range-total.downloading")
	if err := os.WriteFile(target, srcData[:4], 0644); err != nil {
		t.Fatal(err)
	}
	dl := NewStreamDownloader([]string{ts.URL}, target)
	if err := dl.DownloadSingleStream(context.Background(), nil); err != nil {
		t.Fatalf("未知 Content-Range 总长度后恢复下载失败: %v", err)
	}
	actual, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, srcData) {
		t.Fatalf("未知总长度响应不应被追加: got %q, want %q", actual, srcData)
	}
}

func TestStreamDownloader_OversizedFileReportsRollback(t *testing.T) {
	srcData := []byte("valid")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(srcData)))
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(srcData[:1])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(srcData)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(srcData)
	}))
	defer ts.Close()

	target := filepath.Join(t.TempDir(), "oversized-progress.downloading")
	if err := os.WriteFile(target, []byte("stale-data"), 0644); err != nil {
		t.Fatal(err)
	}
	dl := NewStreamDownloader([]string{ts.URL}, target)
	var delta int64
	if err := dl.DownloadSingleStream(nil, func(value int64) { delta += value }); err != nil {
		t.Fatalf("超长临时文件清理后下载失败: %v", err)
	}
	if actual, err := os.ReadFile(target); err != nil || !bytes.Equal(actual, srcData) {
		t.Fatalf("超长临时文件恢复内容错误: got %q err=%v", actual, err)
	}
	if delta != int64(len(srcData))-int64(len("stale-data")) {
		t.Fatalf("超长临时文件进度回退错误: got %d, want %d", delta, len(srcData)-len("stale-data"))
	}
}

func TestStreamDownloader_UnknownLengthIsNotMarkedComplete(t *testing.T) {
	shortData := []byte("truncated")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Range") == "bytes=0-0" {
			http.Error(w, "range probing unavailable", http.StatusBadRequest)
			return
		}
		// Flush 让 net/http 使用无 Content-Length 的分块响应，模拟无法获知总长的流。
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write(shortData)
	}))
	defer ts.Close()

	targetFile := filepath.Join(t.TempDir(), "unknown-length.downloading")
	dl := NewStreamDownloader([]string{ts.URL}, targetFile)
	err := dl.DownloadSingleStream(context.Background(), nil)
	if err == nil {
		t.Fatal("未知总长度的 EOF 不应被标记为下载成功")
	}
	if !strings.Contains(err.Error(), "可验证的总长度") {
		t.Fatalf("未知总长度应返回明确错误，实际: %v", err)
	}
	actual, readErr := os.ReadFile(targetFile)
	if readErr != nil {
		t.Fatalf("读取保留的临时文件失败: %v", readErr)
	}
	if !bytes.Equal(actual, shortData) {
		t.Fatalf("临时文件内容不符合服务端响应: got %q, want %q", actual, shortData)
	}
}

// TestStreamDownloader_416OversizedRecovery 测试本地文件异常过大触发 416 时自动重置并重新下载
func TestStreamDownloader_416OversizedRecovery(t *testing.T) {
	dataSize := 500 * 1024
	srcData := make([]byte, dataSize)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rangeHdr := req.Header.Get("Range")
		if rangeHdr == "bytes=0-0" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", dataSize))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(srcData[:1])
			return
		}

		if strings.HasPrefix(rangeHdr, "bytes=") {
			start, _ := strconv.ParseInt(strings.TrimPrefix(rangeHdr, "bytes="), 10, 64)
			if start >= int64(dataSize) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", dataSize))
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
		}

		w.Header().Set("Content-Length", strconv.Itoa(dataSize))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(srcData)
	}))
	defer ts.Close()

	tmpDir, _ := os.MkdirTemp("", "oversized_*")
	defer os.RemoveAll(tmpDir)

	targetFile := filepath.Join(tmpDir, "oversized.downloading")
	// 预置超出总大小的错误脏文件 (600KB > 500KB)
	oversizedData := make([]byte, 600*1024)
	_ = os.WriteFile(targetFile, oversizedData, 0644)

	dl := NewStreamDownloader([]string{ts.URL}, targetFile)
	err := dl.DownloadSingleStream(context.Background(), nil)
	if err != nil {
		t.Fatalf("超长脏文件恢复失败: %v", err)
	}

	finalData, _ := os.ReadFile(targetFile)
	if len(finalData) != dataSize {
		t.Fatalf("超长脏文件恢复后大小不匹配: 期望 %d, 实际 %d", dataSize, len(finalData))
	}
}

// TestStreamDownloader_MultiCDNFailover 测试多 CDN 自动轮换与故障转移
func TestStreamDownloader_MultiCDNFailover(t *testing.T) {
	dataSize := 100 * 1024
	srcData := make([]byte, dataSize)

	// CDN 1: 模拟故障 (返回 502 Bad Gateway)
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "CDN node down", http.StatusBadGateway)
	}))
	defer ts1.Close()

	// CDN 2: 正常可用
	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", dataSize))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(srcData[:1])
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(dataSize))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(srcData)
	}))
	defer ts2.Close()

	tmpDir, _ := os.MkdirTemp("", "failover_*")
	defer os.RemoveAll(tmpDir)

	targetFile := filepath.Join(tmpDir, "failover.downloading")

	// 候选 CDN 列表：故障节点排第一，正常节点排第二
	dl := NewStreamDownloader([]string{ts1.URL, ts2.URL}, targetFile)
	err := dl.DownloadSingleStream(context.Background(), nil)
	if err != nil {
		t.Fatalf("多 CDN 故障转移失败: %v", err)
	}

	finalData, _ := os.ReadFile(targetFile)
	if len(finalData) != dataSize {
		t.Fatalf("故障转移下载文件大小不匹配: 期望 %d, 实际 %d", dataSize, len(finalData))
	}
}

// TestStreamDownloader_416WithZeroTotalSizeRecovery 红绿灯测试：totalSize <= 0 时遇 416 状态码，旧逻辑无法触发文件清理并耗尽重试报错，新逻辑应能主动清理脏文件自愈完成下载
func TestStreamDownloader_416WithZeroTotalSizeRecovery(t *testing.T) {
	dataSize := 100 * 1024
	srcData := make([]byte, dataSize)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rangeHdr := req.Header.Get("Range")
		// 模拟某些 CDN 探测 bytes=0-0 时不支持 Range，返回 400 或不带 Content-Range
		if rangeHdr == "bytes=0-0" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if strings.HasPrefix(rangeHdr, "bytes=") {
			rangeSpec := strings.TrimPrefix(rangeHdr, "bytes=")
			parts := strings.Split(rangeSpec, "-")
			start, _ := strconv.ParseInt(parts[0], 10, 64)
			// 本地存在脏数据 (200KB > 100KB)，此时请求超出范围返回 416
			if start >= int64(dataSize) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", dataSize))
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
		}

		// 从 0 开始请求时正常返回 200 OK
		w.Header().Set("Content-Length", strconv.Itoa(dataSize))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(srcData)
	}))
	defer ts.Close()

	tmpDir, _ := os.MkdirTemp("", "zero_totalsize_416_*")
	defer os.RemoveAll(tmpDir)

	targetFile := filepath.Join(tmpDir, "dirty.downloading")
	// 预置超长脏数据
	_ = os.WriteFile(targetFile, make([]byte, 200*1024), 0644)

	dl := NewStreamDownloader([]string{ts.URL}, targetFile)
	// 此时 dl.totalSize 未知 (0)
	err := dl.DownloadSingleStream(context.Background(), nil)
	if err != nil {
		t.Fatalf("红灯触发：totalSize 未知时遇 416 失败: %v", err)
	}

	finalData, _ := os.ReadFile(targetFile)
	if len(finalData) != dataSize {
		t.Fatalf("自愈后文件大小不匹配: 期望 %d, 实际 %d", dataSize, len(finalData))
	}
	t.Logf("绿灯：totalSize 未知时遇 416 成功自愈下载")
}

func TestStreamDownloader_ConcurrentRangeDownloadAndCleanup(t *testing.T) {
	data := make([]byte, 2*1024*1024)
	for i := range data {
		data[i] = byte((i*31 + 7) % 251)
	}

	var active, maxActive, rangeRequests int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rangeHeader := req.Header.Get("Range")
		if rangeHeader == "bytes=0-0" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(data)))
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[:1])
			return
		}

		if !strings.HasPrefix(rangeHeader, "bytes=") {
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}

		parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
		if len(parts) != 2 {
			http.Error(w, "invalid range", http.StatusBadRequest)
			return
		}
		start, startErr := strconv.ParseInt(parts[0], 10, 64)
		end, endErr := strconv.ParseInt(parts[1], 10, 64)
		if startErr != nil || endErr != nil || start < 0 || end < start || end >= int64(len(data)) {
			http.Error(w, "invalid range", http.StatusBadRequest)
			return
		}

		atomic.AddInt32(&rangeRequests, 1)
		current := atomic.AddInt32(&active, 1)
		for {
			oldMax := atomic.LoadInt32(&maxActive)
			if current <= oldMax || atomic.CompareAndSwapInt32(&maxActive, oldMax, current) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[start : end+1])
		atomic.AddInt32(&active, -1)
	}))
	defer ts.Close()

	target := filepath.Join(t.TempDir(), "concurrent.downloading")
	dl := NewStreamDownloader([]string{ts.URL}, target)
	var progress int64
	if err := dl.DownloadWithConcurrency(context.Background(), 4, func(delta int64) {
		atomic.AddInt64(&progress, delta)
	}); err != nil {
		t.Fatalf("并发 Range 下载失败: %v", err)
	}

	actual, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, data) {
		t.Fatal("并发分片合并后的文件内容不一致")
	}
	if got := atomic.LoadInt32(&rangeRequests); got != 4 {
		t.Fatalf("预期每个分片各请求一次，实际 Range 请求 %d 次", got)
	}
	if got := atomic.LoadInt32(&maxActive); got < 2 {
		t.Fatalf("预期至少两个分片并行下载，最大并发数仅为 %d", got)
	}
	if got := atomic.LoadInt64(&progress); got != int64(len(data)) {
		t.Fatalf("进度增量不完整: got %d, want %d", got, len(data))
	}
	for i := 0; i < 4; i++ {
		if _, err := os.Stat(streamSegmentPath(target, i)); !os.IsNotExist(err) {
			t.Fatalf("并发下载成功后分片文件仍残留: %s", streamSegmentPath(target, i))
		}
	}
}

func TestStreamDownloader_ConcurrentRangeUnsupportedFallsBackToSingleStream(t *testing.T) {
	data := []byte("server does not support byte ranges, but the full entity is valid")
	var rangedRequests int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Range") == "bytes=0-0" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(data)))
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[:1])
			return
		}
		if req.Header.Get("Range") != "" {
			atomic.AddInt32(&rangedRequests, 1)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer ts.Close()

	target := filepath.Join(t.TempDir(), "range-fallback.downloading")
	dl := NewStreamDownloader([]string{ts.URL}, target)
	if err := dl.DownloadWithConcurrency(context.Background(), 4, nil); err != nil {
		t.Fatalf("Range 不支持时回退单流失败: %v", err)
	}
	actual, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, data) {
		t.Fatalf("回退单流后的内容不一致: got %q, want %q", actual, data)
	}
	if atomic.LoadInt32(&rangedRequests) == 0 {
		t.Fatal("并发路径未实际探测到 Range 不支持，回退测试无效")
	}
	for i := 0; i < 4; i++ {
		if _, err := os.Stat(streamSegmentPath(target, i)); !os.IsNotExist(err) {
			t.Fatalf("回退成功后分片文件仍残留: %s", streamSegmentPath(target, i))
		}
	}
}

func TestStreamDownloader_ConcurrentPreparationHonorsCancellation(t *testing.T) {
	target := filepath.Join(t.TempDir(), "cancelled.downloading")
	if err := os.WriteFile(target, bytes.Repeat([]byte{'x'}, 1024), 0644); err != nil {
		t.Fatal(err)
	}

	dl := NewStreamDownloader([]string{"https://cdn.invalid/media"}, target)
	dl.totalSize = 4096
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := dl.DownloadWithConcurrency(ctx, 4, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("并发下载准备阶段应及时响应取消，实际错误: %v", err)
	}
}
