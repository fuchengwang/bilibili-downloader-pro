package downloader

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamDiagnosticIgnoresSiblingCancellationAndHidesSignedURLs(t *testing.T) {
	s := &StreamDownloader{}
	cause := errors.New("connection reset")
	s.recordDiagnostic(cause)
	s.recordDiagnostic(fmt.Errorf("request: %w", context.Canceled))
	if s.lastDiagnostic() != cause {
		t.Fatal("sibling cancellation replaced the request error")
	}
	detail := streamFailureDetail("视频轨", errors.New(`Get "https://cdn.example/video?token=secret": EOF`), cause)
	if strings.Contains(detail, "secret") || strings.Contains(detail, "cdn.example") || !strings.Contains(detail, "connection reset") {
		t.Fatalf("unsafe or incomplete diagnostic: %s", detail)
	}
}

func TestSingleStreamDiagnosticCapturesLocalFailure(t *testing.T) {
	dir := t.TempDir()
	s := &StreamDownloader{targetPath: filepath.Join(dir, "video.tmp")}
	// No valid URL fails before a network request, and is still diagnosable.
	err := s.DownloadSingleStream(context.Background(), nil)
	if err == nil || s.lastDiagnostic() == nil {
		t.Fatal("single-stream failure lost its diagnostic")
	}
}
