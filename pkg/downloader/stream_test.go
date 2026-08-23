package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

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
	dataSize := 1024 * 1024
	srcData := make([]byte, dataSize)
	for i := range srcData {
		srcData[i] = byte(i % 256)
	}

	var requestCount int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		rangeHdr := req.Header.Get("Range")

		// 第一次带 Range 请求时，故意返回起点为 0 的异常 206
		if count == 1 && rangeHdr != "" && rangeHdr != "bytes=0-0" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", dataSize-1, dataSize))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(srcData)
			return
		}

		// 正常处理
		if rangeHdr == "bytes=0-0" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", dataSize))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(srcData[:1])
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
	// 预置脏数据
	_ = os.WriteFile(targetFile, []byte("invalid partial data"), 0644)

	dl := NewStreamDownloader([]string{ts.URL}, targetFile)
	err := dl.DownloadSingleStream(context.Background(), nil)
	if err != nil {
		t.Fatalf("错位恢复下载失败: %v", err)
	}

	finalData, _ := os.ReadFile(targetFile)
	if len(finalData) != dataSize {
		t.Fatalf("错位恢复后文件大小错误: 期望 %d, 实际 %d", dataSize, len(finalData))
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
