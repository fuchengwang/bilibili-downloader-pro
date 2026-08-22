package downloader

import (
	"context"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

func TestLiveParseAndStreamResolve(t *testing.T) {
	client := bilibili.GetDefaultClient()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 使用一个公开的经典 B 站测试视频 (如 BV1xx411c7mD 或 av170001)
	parsed, err := client.ParseInput(ctx, "BV1xx411c7mD")
	if err != nil {
		t.Fatalf("ParseInput failed: %v", err)
	}

	detail, err := client.FetchVideoDetail(ctx, parsed)
	if err != nil {
		t.Fatalf("FetchVideoDetail failed: %v", err)
	}

	if detail.Title == "" {
		t.Errorf("Expected non-empty title, got empty")
	}
	if len(detail.Episodes) == 0 {
		t.Fatalf("Expected at least 1 episode, got 0")
	}

	t.Logf("Video Title: %s, Total Parts: %d, Cover: %s", detail.Title, detail.TotalParts, detail.Cover)

	// 测试清晰度列表获取
	ep := detail.Episodes[0]
	qualities, err := client.GetAvailableQualities(ctx, ep.BVID, ep.AID, ep.CID, ep.EPID, false)
	if err != nil {
		t.Fatalf("GetAvailableQualities failed: %v", err)
	}
	if len(qualities) == 0 {
		t.Fatalf("Expected qualities list, got 0")
	}

	t.Logf("Found %d available qualities for P1", len(qualities))
	for _, q := range qualities {
		t.Logf("  - QN %d: %s (Available: %v, VIP: %v)", q.ID, q.Label, q.IsAvailable, q.IsVipRequired)
	}

	// 测试流选定
	sel, err := client.FetchStreamSelection(ctx, ep.BVID, ep.AID, ep.CID, ep.EPID, false, "highest", "auto")
	if err != nil {
		t.Fatalf("FetchStreamSelection failed: %v", err)
	}

	if sel.VideoURL == "" {
		t.Errorf("Expected valid video URL, got empty")
	}
	t.Logf("Selected Stream: %s (%s, %dx%d)", sel.QualityLabel, sel.Codec, sel.Width, sel.Height)
}
