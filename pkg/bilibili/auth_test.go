package bilibili

import (
	"context"
	"testing"
	"time"
)

func TestParseAndSaveRawCookie(t *testing.T) {
	client := GetDefaultClient()

	// 1. 测试正常包含必要字段的 Cookie 字符串解析
	rawValid := "SESSDATA=test_sess_12345; bili_jct=csrf_token_abc; buvid3=buvid_xyz_789; DedeUserID=12345678; other_key=other_value"
	err := client.ParseAndSaveRawCookie(rawValid)
	if err != nil {
		t.Fatalf("ParseAndSaveRawCookie 失败: %v", err)
	}

	cookies := client.GetCookies()
	if cookies == nil {
		t.Fatal("GetCookies 返回 nil")
	}
	if cookies.SessData != "test_sess_12345" {
		t.Errorf("预期 SessData 为 test_sess_12345，实际为: %s", cookies.SessData)
	}
	if cookies.BiliJCT != "csrf_token_abc" {
		t.Errorf("预期 BiliJCT 为 csrf_token_abc，实际为: %s", cookies.BiliJCT)
	}
	if cookies.Buvid3 != "buvid_xyz_789" {
		t.Errorf("预期 Buvid3 为 buvid_xyz_789，实际为: %s", cookies.Buvid3)
	}
	if cookies.DedeUID != "12345678" {
		t.Errorf("预期 DedeUID 为 12345678，实际为: %s", cookies.DedeUID)
	}
	if cookies.Cookies["other_key"] != "other_value" {
		t.Errorf("预期 other_key 为 other_value，实际为: %s", cookies.Cookies["other_key"])
	}

	// 2. 测试缺失 SESSDATA 时的错误拦截
	rawInvalid := "bili_jct=csrf_only; DedeUserID=12345"
	err = client.ParseAndSaveRawCookie(rawInvalid)
	if err == nil {
		t.Fatal("缺失 SESSDATA 应当返回错误，但返回了 nil")
	}

	// 3. 测试空字符串输入拦截
	err = client.ParseAndSaveRawCookie("   ")
	if err == nil {
		t.Fatal("空 Cookie 应当返回错误，但返回了 nil")
	}

	// 清理测试用的 Cookie
	_ = client.ClearCookies()
}

func TestFetchFingerprintSpiLive(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping live API call in short mode")
	}

	client := GetDefaultClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	b3, b4, err := client.fetchFingerprintSpi(ctx)
	if err != nil {
		t.Fatalf("fetchFingerprintSpi 实机调用失败: %v", err)
	}

	if b3 == "" {
		t.Fatal("预期返回非空 buvid3 (b_3)")
	}
	t.Logf("✓ B 站指纹接口实测成功: buvid3=%s, buvid4=%s", b3, b4)
}

func TestQRCodeGenerateLive(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping live API call in short mode")
	}

	client := GetDefaultClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	qr, err := client.GenerateQRCode(ctx)
	if err != nil {
		t.Fatalf("GenerateQRCode 实机调用失败: %v", err)
	}

	if qr.URL == "" || qr.QRCodeKey == "" {
		t.Fatalf("生成的二维码数据异常: URL=%s, Key=%s", qr.URL, qr.QRCodeKey)
	}
	t.Logf("✓ 二维码生成接口实测成功: Key=%s, URL=%s", qr.QRCodeKey, qr.URL)
}
