//go:build !windows

package downloader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"bilibili_downloader/pkg/config"
)

// A FIFO deterministically models a directory open which never returns (for
// example a protected folder waiting for OS consent or a disconnected mount).
func TestManagerStartupDoesNotOpenUnreferencedDownloadDirectory(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "paused visible download"}[pending], func(t *testing.T) {
			dir := t.TempDir()
			blocked := filepath.Join(dir, "blocked-destination")
			if err := syscall.Mkfifo(blocked, 0600); err != nil {
				t.Fatal(err)
			}
			cfg := config.NewConfigManager(filepath.Join(dir, "config"))
			settings := cfg.Get()
			settings.DownloadDir = blocked
			if err := cfg.Save(settings); err != nil {
				t.Fatal(err)
			}
			if pending {
				output := filepath.Join(blocked, "video.mp4")
				video, audio := downloadPaths(output)
				data, err := json.Marshal([]*DownloadTask{{ID: newUUID(), Status: StatusPaused, OutputPath: output, VideoTmpPath: video, AudioTmpPath: audio}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(cfg.GetTasksPath(), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan struct{})
			go func() { NewDownloadManager(cfg); close(done) }()
			select {
			case <-done:
			case <-time.After(500 * time.Millisecond):
				// Unblock a regressed reader before failing, so no goroutine outlives
				// its temporary directory. Nonblocking open cannot hang a passing test.
				if fd, err := syscall.Open(blocked, os.O_WRONLY|syscall.O_NONBLOCK, 0600); err == nil {
					_ = syscall.Close(fd)
				}
				select {
				case <-done:
				case <-time.After(time.Second):
				}
				t.Fatal("startup tried to enumerate the download destination")
			}
		})
	}
}
