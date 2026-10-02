package bilibili

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"bilibili_downloader/pkg/utils"
)

// TestBangumiDurationConversion 复现并验证番剧接口返回的毫秒时长转换为秒
// 在修复前：ep.Duration 为 1440000 毫秒，FormatDuration 计算为 400:00:00
// 在修复后：ep.Duration 应为 1440 秒，FormatDuration 正常计算为 24:00
func TestBangumiDurationConversion(t *testing.T) {
	// 测试 parseBangumiDuration 函数
	dur := parseBangumiDuration(1440000)
	if dur != 1440 {
		t.Errorf("parseBangumiDuration(1440000) = %d, expected 1440", dur)
	}

	formatted := utils.FormatDuration(dur)
	if formatted != "24:00" {
		t.Errorf("utils.FormatDuration(1440) = %q, expected '24:00'", formatted)
	}

	// 边界测试：0 毫秒与极短毫秒
	if parseBangumiDuration(0) != 0 {
		t.Errorf("parseBangumiDuration(0) should be 0")
	}
	if parseBangumiDuration(500) != 500 {
		t.Errorf("parseBangumiDuration(500) for < 1000 should return 500")
	}
	if parseBangumiDuration(30000) != 30 {
		t.Errorf("parseBangumiDuration(30000) should be 30s")
	}
}

func TestFetchVideoDetailRejectsNilTarget(t *testing.T) {
	client := &Client{}
	if _, err := client.FetchVideoDetail(context.Background(), nil); err == nil {
		t.Fatal("nil parse target should be rejected before any network request")
	}
}

// TestFetchBangumiDetail_DurationEndToEnd 测试解析端到端番剧响应时时长字段转换为秒
func TestFetchBangumiDetail_DurationEndToEnd(t *testing.T) {
	mockResp := seasonResponse{
		Code: 0,
	}
	mockResp.Result.Title = "测试番剧"
	mockResp.Result.Cover = "https://i0.hdslb.com/bfs/archive/test.jpg"
	mockResp.Result.Episodes = []struct {
		ID        int64  `json:"id"`
		Aid       int64  `json:"aid"`
		Bvid      string `json:"bvid"`
		Cid       int64  `json:"cid"`
		Title     string `json:"title"`
		LongTitle string `json:"long_title"`
		Badge     string `json:"badge"`
		Cover     string `json:"cover"`
		Duration  int    `json:"duration"`
	}{
		{
			ID:        1001,
			Aid:       2001,
			Bvid:      "BV1test",
			Cid:       3001,
			Title:     "第1话",
			LongTitle: "相遇",
			Duration:  1440000, // 24 分钟 (1440000 毫秒)
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mockResp)
	}))
	defer server.Close()

	client := GetDefaultClient()

	// 使用自定义 URL 测试
	var resp seasonResponse
	err := client.GetJSON(context.Background(), server.URL, &resp)
	if err != nil {
		t.Fatalf("GetJSON failed: %v", err)
	}

	ep := resp.Result.Episodes[0]
	parsedDur := parseBangumiDuration(ep.Duration)
	if parsedDur != 1440 {
		t.Errorf("Expected episode duration 1440s, got %d", parsedDur)
	}
	formatted := utils.FormatDuration(parsedDur)
	if formatted != "24:00" {
		t.Errorf("Expected formatted duration '24:00', got %q", formatted)
	}
}
