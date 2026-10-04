package downloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
	"bilibili_downloader/pkg/config"
)

// Explicit opt-in: uses the locally signed-in account and isolated output/cache
// directories. Normal CI tests neither contact the service nor download courses.
func TestLiveCheeseDownload(t *testing.T) {
	episodes := os.Getenv("BBDOWN_CHEESE_TEST_EPISODES")
	if episodes == "" || testing.Short() {
		t.Skip("set BBDOWN_CHEESE_TEST_EPISODES to verify authorized classroom downloads")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client := bilibili.GetDefaultClient()
	user, err := client.GetUserInfo(ctx)
	if err != nil || user == nil || !user.IsLogin {
		t.Fatal("live course validation requires the signed-in purchase account")
	}
	t.Logf("Signed-in account: %s", user.Uname)
	dir := os.Getenv("BBDOWN_CHEESE_TEST_OUTPUT")
	if dir == "" {
		dir = t.TempDir()
	}
	manager := NewDownloadManager(config.NewConfigManager(dir))
	for _, id := range strings.Split(episodes, ",") {
		epid, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
		if err != nil || epid <= 0 {
			t.Fatal("invalid test episode")
		}
		target := &bilibili.ParsedTarget{Type: bilibili.TargetCheese, EPID: fmt.Sprint(epid)}
		detail, err := client.FetchVideoDetail(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		var episode *bilibili.EpisodeInfo
		for index := range detail.Episodes {
			if detail.Episodes[index].EPID == epid {
				episode = &detail.Episodes[index]
				break
			}
		}
		if episode == nil {
			t.Fatal("linked course lesson missing from catalogue")
		}
		t.Logf("Course: %s; catalogue: %d lessons; selected: %s (%s, %s)", detail.Title, detail.TotalParts, episode.Title, episode.DurationStr, episode.Badge)
		req := &DownloadRequest{IsCheese: true, SSID: detail.SeasonID, Title: detail.Title, TargetQuality: "highest", TargetCodec: "auto", Episodes: []int64{episode.CID}}
		task, err := manager.AddDownloadTask(req, episode)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status != StatusCompleted {
			manager.mu.Lock()
			task.Status = StatusDownloading
			manager.workers[task.ID] = workerHandle{token: "live-course-validation", cancel: cancel}
			manager.mu.Unlock()
			done := make(chan struct{})
			go func() {
				manager.runTask(ctx, task, "live-course-validation")
				close(done)
			}()
			ticker := time.NewTicker(10 * time.Second)
		loop:
			for {
				select {
				case <-done:
					break loop
				case <-ctx.Done():
					<-done
					t.Fatal("classroom download timed out")
				case <-ticker.C:
					for _, snapshot := range manager.GetTasks() {
						if snapshot.ID == task.ID {
							t.Logf("%s: %s %.1f%%, %s", snapshot.PartTitle, snapshot.Status, snapshot.Progress, snapshot.SpeedStr)
						}
					}
				}
			}
			ticker.Stop()
		}
		if task.Status != StatusCompleted {
			t.Fatalf("lesson failed: %s: %s", task.Status, task.ErrorMsg)
		}
		info, err := os.Stat(task.OutputPath)
		if err != nil || info.Size() == 0 {
			t.Fatal("completed course output missing")
		}
		if filepath.Ext(task.OutputPath) != ".mp4" {
			t.Fatal("course output has wrong container")
		}
		t.Logf("Completed MP4: %s (%d bytes)", task.OutputPath, info.Size())
	}
}
