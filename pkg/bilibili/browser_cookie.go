package bilibili

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/crypto/pbkdf2"
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

// TryReadBrowserCookies 遍历并尝试读取各浏览器的 Bilibili Cookie
func TryReadBrowserCookies() (map[string]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	var allCookies = make(map[string]string)

	if runtime.GOOS == "darwin" {
		// 1. Google Chrome (macOS)
		chromeDirs := []string{
			filepath.Join(home, "Library/Application Support/Google/Chrome/Default"),
			filepath.Join(home, "Library/Application Support/Google/Chrome/Profile 1"),
			filepath.Join(home, "Library/Application Support/Google/Chrome/Profile 2"),
			filepath.Join(home, "Library/Application Support/Google/Chrome/Profile 3"),
		}
		key, _ := getMacChromeKey("Chrome Safe Storage")
		for _, dir := range chromeDirs {
			if ck := readChromiumCookies(dir, key); ck != nil && ck["SESSDATA"] != "" {
				return ck, nil
			}
		}

		// 2. Microsoft Edge (macOS)
		edgeDirs := []string{
			filepath.Join(home, "Library/Application Support/Microsoft Edge/Default"),
			filepath.Join(home, "Library/Application Support/Microsoft Edge/Profile 1"),
		}
		edgeKey, _ := getMacChromeKey("Microsoft Edge Safe Storage")
		for _, dir := range edgeDirs {
			if ck := readChromiumCookies(dir, edgeKey); ck != nil && ck["SESSDATA"] != "" {
				return ck, nil
			}
		}

		// 3. Arc Browser (macOS)
		arcDirs := []string{
			filepath.Join(home, "Library/Application Support/Arc/User Data/Default"),
		}
		arcKey, _ := getMacChromeKey("Arc Safe Storage")
		for _, dir := range arcDirs {
			if ck := readChromiumCookies(dir, arcKey); ck != nil && ck["SESSDATA"] != "" {
				return ck, nil
			}
		}

		// 4. Firefox (macOS)
		ffDir := filepath.Join(home, "Library/Application Support/Firefox/Profiles")
		if ck := readFirefoxCookies(ffDir); ck != nil && ck["SESSDATA"] != "" {
			return ck, nil
		}
	} else if runtime.GOOS == "windows" {
		// Windows: Chrome, Edge
		localApp := os.Getenv("LOCALAPPDATA")
		appData := os.Getenv("APPDATA")

		chromeDir := filepath.Join(localApp, `Google\Chrome\User Data\Default`)
		if ck := readWindowsChromiumCookies(chromeDir); ck != nil && ck["SESSDATA"] != "" {
			return ck, nil
		}

		edgeDir := filepath.Join(localApp, `Microsoft\Edge\User Data\Default`)
		if ck := readWindowsChromiumCookies(edgeDir); ck != nil && ck["SESSDATA"] != "" {
			return ck, nil
		}

		ffDir := filepath.Join(appData, `Mozilla\Firefox\Profiles`)
		if ck := readFirefoxCookies(ffDir); ck != nil && ck["SESSDATA"] != "" {
			return ck, nil
		}
	}

	if len(allCookies) > 0 && allCookies["SESSDATA"] != "" {
		return allCookies, nil
	}

	return nil, fmt.Errorf("未能检测到已登录的浏览器 Cookie，请尝试手机扫码登录或手动填入 Cookie")
}

func getMacChromeKey(serviceName string) ([]byte, error) {
	cmd := exec.Command("security", "find-generic-password", "-w", "-s", serviceName)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	pass := strings.TrimSpace(string(out))
	if pass == "" {
		return nil, fmt.Errorf("empty password from keychain")
	}

	// PBKDF2-HMAC-SHA1 with salt "saltysalt", 1003 iterations, 16-byte key
	key := pbkdf2.Key([]byte(pass), []byte("saltysalt"), 1003, 16, sha1.New)
	return key, nil
}

func readChromiumCookies(profileDir string, key []byte) map[string]string {
	cookiePath := filepath.Join(profileDir, "Cookies")
	if _, err := os.Stat(cookiePath); err != nil {
		cookiePath = filepath.Join(profileDir, "Network", "Cookies")
		if _, err := os.Stat(cookiePath); err != nil {
			return nil
		}
	}

	// 复制到临时文件以防止数据库被占用锁定
	tmpFile, err := os.CreateTemp("", "bili_cookie_*.db")
	if err != nil {
		return nil
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	src, err := os.Open(cookiePath)
	if err != nil {
		return nil
	}
	dst, err := os.Create(tmpPath)
	if err != nil {
		src.Close()
		return nil
	}
	_, _ = io.Copy(dst, src)
	src.Close()
	dst.Close()

	db, err := sql.Open("sqlite3", tmpPath+"?mode=ro")
	if err != nil {
		return nil
	}
	defer db.Close()

	rows, err := db.Query("SELECT name, encrypted_value, value FROM cookies WHERE host_key LIKE '%bilibili.com%'")
	if err != nil {
		return nil
	}
	defer rows.Close()

	cookies := make(map[string]string)
	for rows.Next() {
		var name, val string
		var encVal []byte
		if err := rows.Scan(&name, &encVal, &val); err != nil {
			continue
		}
		if val != "" {
			cookies[name] = val
		} else if len(encVal) > 0 && len(key) > 0 {
			dec := decryptMacChromeCookie(encVal, key)
			if dec != "" {
				cookies[name] = dec
			}
		}
	}

	return cookies
}

func decryptMacChromeCookie(encVal []byte, key []byte) string {
	if len(encVal) < 3 {
		return ""
	}
	// Chrome macOS cookie starts with "v10"
	if bytes.HasPrefix(encVal, []byte("v10")) {
		encVal = encVal[3:]
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}

	if len(encVal)%aes.BlockSize != 0 {
		return ""
	}

	iv := bytes.Repeat([]byte(" "), aes.BlockSize) // 16 spaces
	mode := cipher.NewCBCDecrypter(block, iv)
	dec := make([]byte, len(encVal))
	mode.CryptBlocks(dec, encVal)

	// PKCS7 unpad
	if len(dec) == 0 {
		return ""
	}
	padLen := int(dec[len(dec)-1])
	if padLen > 0 && padLen <= aes.BlockSize && padLen <= len(dec) {
		dec = dec[:len(dec)-padLen]
	}

	return string(dec)
}

func readWindowsChromiumCookies(profileDir string) map[string]string {
	// Windows DPAPI / CryptUnprotectData fallback or plaintext value
	return nil
}

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
		defer rows.Close()

		cookies := make(map[string]string)
		for rows.Next() {
			var name, val string
			if err := rows.Scan(&name, &val); err == nil && val != "" {
				cookies[name] = val
			}
		}

		if cookies["SESSDATA"] != "" {
			return cookies
		}
	}

	return nil
}
