package downloader

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
	"bilibili_downloader/pkg/config"
)

func TestRealVideoDownload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping live download test in short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	biliClient := bilibili.GetDefaultClient()

	tmpDir := t.TempDir()
	testDownloadDir := filepath.Join(tmpDir, "downloads")
	_ = os.MkdirAll(testDownloadDir, 0755)

	cfgMgr := config.NewConfigManager(tmpDir)
	testCfg := cfgMgr.Get()
	testCfg.DownloadDir = testDownloadDir
	testCfg.ThreadsPerTask = 4
	_ = cfgMgr.Save(testCfg)

	// 1. 解析目标链接
	url := "https://www.bilibili.com/video/BV1m1Lv6pEiN/"
	fmt.Printf("\n=== [1] 开始解析真实链接: %s ===\n", url)
	target, err := biliClient.ParseInput(ctx, url)
	if err != nil {
		t.Fatalf("输入解析失败: %v", err)
	}

	detail, err := biliClient.FetchVideoDetail(ctx, target)
	if err != nil {
		t.Fatalf("元数据获取失败: %v", err)
	}

	fmt.Printf("标题: %s\n", detail.Title)
	fmt.Printf("UP主: %s\n", detail.OwnerName)
	fmt.Printf("是否合集/多P: %v, 总分集数: %d\n", detail.IsCollection, detail.TotalParts)
	for i, ep := range detail.Episodes {
		fmt.Printf("  分P %d: %s (时长: %s, CID: %d)\n", ep.Index, ep.Title, ep.DurationStr, ep.CID)
		if i >= 4 {
			fmt.Printf("  ... (共 %d 集)\n", len(detail.Episodes))
			break
		}
	}

	numToTest := 2
	if len(detail.Episodes) < numToTest {
		numToTest = len(detail.Episodes)
	}

	manager := NewDownloadManager(cfgMgr)
	go manager.schedulerLoop()

	fmt.Printf("\n=== [2] 添加 %d 个下载任务到下载队列 ===\n", numToTest)
	manager.SetCallback(func(task *DownloadTask) {
		if task.Status == StatusDownloading {
			fmt.Printf("\r[下载中] %s | 进度: %.1f%% | 速度: %s | 预估剩余: %s",
				task.PartTitle, task.Progress, task.SpeedStr, task.ETAStr)
		} else if task.Status == StatusMerging {
			fmt.Printf("\n[合成中] %s -> 纯 Go 原生引擎正在封装音视频...\n", task.PartTitle)
		} else if task.Status == StatusCompleted {
			fmt.Printf("\n[已完成] %s -> 保存至: %s\n", task.PartTitle, task.OutputPath)
		} else if task.Status == StatusError {
			fmt.Printf("\n[出错了] %s -> 报错: %s\n", task.PartTitle, task.ErrorMsg)
		}
	})

	var tasks []*DownloadTask
	for i := 0; i < numToTest; i++ {
		ep := detail.Episodes[i]
		req := &DownloadRequest{
			BVID:          detail.BVID,
			AID:           detail.AID,
			Title:         detail.Title,
			Cover:         detail.Cover,
			IsBangumi:     detail.Type == "bangumi",
			TargetQuality: "highest",
			TargetCodec:   "auto",
		}
		task, err := manager.AddDownloadTask(req, &ep)
		if err != nil {
			t.Fatalf("添加任务失败: %v", err)
		}
		tasks = append(tasks, task)
	}

	// 3. 等待所有测试任务完成
	fmt.Printf("已启动后台下载任务，正在测试下载平稳性与自动合成...\n")
	startTime := time.Now()
	for {
		time.Sleep(500 * time.Millisecond)

		allDone := true
		for _, taskObj := range tasks {
			var currentTask *DownloadTask
			for _, t := range manager.GetTasks() {
				if t.ID == taskObj.ID {
					currentTask = t
					break
				}
			}
			if currentTask != nil {
				if currentTask.Status == StatusDownloading || currentTask.Status == StatusQueued || currentTask.Status == StatusMerging {
					allDone = false
					break
				}
			}
		}

		if allDone {
			break
		}

		if time.Since(startTime) > 2*time.Minute {
			t.Fatalf("测试超时（超过2分钟）")
		}
	}

	fmt.Printf("\n=== [3] 下载结果与最终生成文件检查 ===\n")
	for _, taskObj := range tasks {
		var currentTask *DownloadTask
		for _, t := range manager.GetTasks() {
			if t.ID == taskObj.ID {
				currentTask = t
				break
			}
		}
		if currentTask == nil {
			continue
		}
		if currentTask.Status != StatusCompleted {
			t.Errorf("任务 %s 未成功完成，状态: %s, 报错: %s", currentTask.PartTitle, currentTask.Status, currentTask.ErrorMsg)
		} else {
			fi, err := os.Stat(currentTask.OutputPath)
			if err != nil {
				t.Errorf("输出文件不存在: %s", currentTask.OutputPath)
			} else {
				fmt.Printf("✓ 校验成功: %s (大小: %.2f MB, 清晰度: %s)\n", filepath.Base(currentTask.OutputPath), float64(fi.Size())/1024/1024, currentTask.QualityLabel)
			}
		}
	}
}
