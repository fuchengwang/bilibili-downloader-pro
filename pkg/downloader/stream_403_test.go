package downloader

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestStreamDownloader_403_Loop_Prevention(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	tmpFile := t.TempDir() + "/test.mp4"
	defer os.Remove(tmpFile)

	s := NewStreamDownloader([]string{server.URL}, tmpFile)

	refreshCount := 0
	s.SetURLRefresher(func(ctx context.Context) ([]string, error) {
		refreshCount++
		// Return exactly the same URL (simulating cached playurl)
		return []string{server.URL}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := s.DownloadSingleStream(ctx, nil)

	// Because the URL is identical, attempt should NOT be reset to 0.
	// It should reach maxRetries (15) and fail with "URL auth expired" or "HTTP error".
	if err == nil {
		t.Fatal("Expected error after 15 retries, got nil")
	}

	// Should attempt maxRetries times (around 15)
	if requestCount < 10 || requestCount > 20 {
		t.Errorf("Expected around 15 HTTP requests, got %d", requestCount)
	}

	// Should refresh multiple times
	if refreshCount < 10 || refreshCount > 20 {
		t.Errorf("Expected around 15 refreshes, got %d", refreshCount)
	}
}

func TestStreamDownloader_403_Successful_Refresh_Reset_Attempt(t *testing.T) {
	requestCount := 0

	// Mock a server that returns 403 for URL 1, and 200 for URL 2.
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server1.Close()

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-9/10")
		w.WriteHeader(http.StatusPartialContent)
		io.WriteString(w, "0123456789")
	}))
	defer server2.Close()

	tmpFile := t.TempDir() + "/test2.mp4"
	defer os.Remove(tmpFile)

	s := NewStreamDownloader([]string{server1.URL}, tmpFile)

	refreshCount := 0
	s.SetURLRefresher(func(ctx context.Context) ([]string, error) {
		refreshCount++
		// Return the new valid URL
		return []string{server2.URL}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := s.DownloadSingleStream(ctx, nil)

	if err != nil {
		t.Fatalf("Expected successful download, got error: %v", err)
	}

	if refreshCount != 1 {
		t.Errorf("Expected exactly 1 refresh, got %d", refreshCount)
	}
}

func TestStreamDownloader_403_RefreshFallsBackWhenPrimaryStillForbidden(t *testing.T) {
	expected := []byte("valid content from the backup CDN")
	primaryRequests := 0
	backupRequests := 0

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryRequests++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer primary.Close()

	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupRequests++
		w.Header().Set("Content-Length", strconv.Itoa(len(expected)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(expected)
	}))
	defer backup.Close()

	tmpFile := filepath.Join(t.TempDir(), "fallback.mp4")
	dl := NewStreamDownloader([]string{primary.URL}, tmpFile)
	refreshCount := 0
	dl.SetURLRefresher(func(context.Context) ([]string, error) {
		refreshCount++
		// 刷新后首个节点仍是刚刚失败的节点，备用节点才是可用节点。
		return []string{primary.URL, backup.URL}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := dl.DownloadSingleStream(ctx, nil); err != nil {
		t.Fatalf("403 后切换备用 CDN 失败: %v", err)
	}

	actual, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("读取备用 CDN 下载结果失败: %v", err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("备用 CDN 文件内容不一致: got %q, want %q", actual, expected)
	}
	if refreshCount != 1 {
		t.Fatalf("预期只刷新一次直链，实际刷新 %d 次", refreshCount)
	}
	if backupRequests != 1 {
		t.Fatalf("预期备用 CDN 请求一次，实际请求 %d 次", backupRequests)
	}
	if primaryRequests < 2 {
		t.Fatalf("预期至少包含探测和实际请求，主 CDN 请求 %d 次", primaryRequests)
	}
}
