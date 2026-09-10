package downloader

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
		w.Header().Set("Content-Range", "bytes 0-10/10")
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
