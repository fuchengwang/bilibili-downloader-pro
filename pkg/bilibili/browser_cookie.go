package bilibili

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

// ExtractCookiesFromBrowser 尝试从本机已安装的 Chrome / Edge / Firefox 等浏览器中提取 Bilibili 登录 Cookie
func (c *Client) ExtractCookiesFromBrowser(ctx context.Context) (*UserInfo, error) {
	cookies, err := TryReadBrowserCookies()
	if err != nil {
		return nil, err
	}

	if cookies == nil || cookies["SESSDATA"] == "" {
		return nil, fmt.Errorf("未能从浏览器中找到有效的 B站 登录状态（SESSDATA）")
	}

	cookieData := &CookieData{
		SessData: cookies["SESSDATA"],
		BiliJCT:  cookies["bili_jct"],
		Buvid3:   cookies["buvid3"],
		DedeUID:  cookies["DedeUserID"],
		Cookies:  cookies,
	}

	if err := c.SetCookies(cookieData); err != nil {
		return nil, fmt.Errorf("保存提取到的 Cookie 失败: %w", err)
	}

	// 验证提取到的 Cookie
	user, err := c.GetUserInfo(ctx)
	if err != nil || !user.IsLogin {
		_ = c.ClearCookies()
		return nil, fmt.Errorf("浏览器中的 Cookie 已失效或未登录，请在浏览器中登录 B 站后再试")
	}

	return user, nil
}

// TryReadBrowserCookies 调用平台特定的浏览器 Cookie 提取实现
func TryReadBrowserCookies() (map[string]string, error) {
	return readPlatformBrowserCookies()
}

// readFirefoxCookies 跨平台的 Firefox 数据库读取通用方法
func readFirefoxCookies(profilesDir string) map[string]string {
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dbPath := filepath.Join(profilesDir, entry.Name(), "cookies.sqlite")
		if _, err := os.Stat(dbPath); err != nil {
			continue
		}

		tmpFile, err := os.CreateTemp("", "ff_cookie_*.db")
		if err != nil {
			continue
		}
		tmpPath := tmpFile.Name()
		tmpFile.Close()
		defer os.Remove(tmpPath)

		src, err := os.Open(dbPath)
		if err != nil {
			continue
		}
		dst, err := os.Create(tmpPath)
		if err != nil {
			src.Close()
			continue
		}
		_, _ = io.Copy(dst, src)
		src.Close()
		dst.Close()

		db, err := sql.Open("sqlite3", tmpPath+"?mode=ro")
		if err != nil {
			continue
		}
		defer db.Close()

		rows, err := db.Query("SELECT name, value FROM moz_cookies WHERE host LIKE '%bilibili.com%'")
		if err != nil {
			continue
		}

		cookies := make(map[string]string)
		for rows.Next() {
			var name, val string
			if err := rows.Scan(&name, &val); err == nil {
				cookies[name] = val
			}
		}
		rows.Close()

		if cookies["SESSDATA"] != "" {
			return cookies
		}
	}

	return nil
}
