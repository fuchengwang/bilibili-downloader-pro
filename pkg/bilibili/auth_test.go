package bilibili

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseAndSaveRawCookie(t *testing.T) {
	client := &Client{
		cookiePath: filepath.Join(t.TempDir(), "cookies.json"),
		cookieData: &CookieData{Cookies: make(map[string]string)},
	}

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

}

func TestCookieSnapshotDoesNotExposeMutableMap(t *testing.T) {
	client := &Client{cookieData: &CookieData{
		SessData: "session",
		Cookies:  map[string]string{"custom": "original"},
	}}

	snapshot := client.GetCookies()
	if snapshot == nil {
		t.Fatal("GetCookies returned nil")
	}
	snapshot.Cookies["custom"] = "changed outside client"
	snapshot.Cookies["injected"] = "must not leak"

	if header := client.GetCookieHeader(); header != "SESSDATA=session; custom=original" {
		t.Fatalf("cookie snapshot exposed internal map: %q", header)
	}
}

func TestClearCookiesKeepsMemoryWhenPersistenceRemovalFails(t *testing.T) {
	blockedPath := filepath.Join(t.TempDir(), "cookies.json")
	if err := os.Mkdir(blockedPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blockedPath, "keep"), []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}

	client := &Client{
		cookiePath: blockedPath,
		cookieData: &CookieData{
			SessData: "must-remain",
			Cookies:  map[string]string{"custom": "value"},
		},
	}
	if err := client.ClearCookies(); err == nil {
		t.Fatal("删除被占用的 Cookie 存储路径应返回错误")
	}
	if got := client.GetCookies(); got == nil || got.SessData != "must-remain" || got.Cookies["custom"] != "value" {
		t.Fatalf("Cookie 持久化删除失败时不应清空内存凭证: %+v", got)
	}
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
