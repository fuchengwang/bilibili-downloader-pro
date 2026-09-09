package bilibili

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestWbiClockOffsetCompensation 测试客户端本地时钟偏差时，通过服务端 Date 自动校准补偿 wts 时间戳
func TestWbiClockOffsetCompensation(t *testing.T) {
	client := GetDefaultClient()

	// 模拟当前本地时间比服务器慢 600 秒 (10 分钟)
	serverTime := time.Now().Add(600 * time.Second).UTC()
	dateStr := serverTime.Format(time.RFC1123)

	client.UpdateClockOffset(dateStr)

	offset := client.GetClockOffset()
	if offset < 598 || offset > 602 {
		t.Fatalf("时钟偏移校准异常，预期约 600 秒，实际: %d", offset)
	}

	ctx := context.Background()
	params := map[string]string{
		"aid": "123456",
		"bvid": "BV1xx411c7mD",
	}

	signedQuery, err := client.SignWbiParams(ctx, params)
	if err != nil {
		t.Fatalf("SignWbiParams failed: %v", err)
	}

	vals, err := url.ParseQuery(signedQuery)
	if err != nil {
		t.Fatalf("ParseQuery failed: %v", err)
	}

	wtsStr := vals.Get("wts")
	if wtsStr == "" {
		t.Fatalf("生成的签名缺少 wts 字段")
	}

	wts, err := strconv.ParseInt(wtsStr, 10, 64)
	if err != nil {
		t.Fatalf("解析 wts 失败: %v", err)
	}

	nowUnix := time.Now().Unix()
	diff := wts - nowUnix
	// 校验生成的 wts 时间戳精准融合了 ~600 秒偏移量
	if diff < 598 || diff > 602 {
		t.Fatalf("wts 未正确应用服务器时间偏差补偿: wts=%d, now=%d, diff=%d", wts, nowUnix, diff)
	}

	if !strings.Contains(signedQuery, "&w_rid=") {
		t.Fatalf("生成的签名串缺少 &w_rid=")
	}

	// 还原偏移
	client.UpdateClockOffset(time.Now().Format(time.RFC1123))
}
