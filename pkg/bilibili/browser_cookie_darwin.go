//go:build darwin

package bilibili

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/pbkdf2"
	_ "github.com/mattn/go-sqlite3"
)

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

func readMacChromiumUserData(userDataDir string, serviceName string) map[string]string {
	key, _ := getMacChromeKey(serviceName)
	entries, err := os.ReadDir(userDataDir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dirName := entry.Name()
		if dirName == "Default" || strings.HasPrefix(dirName, "Profile") || dirName == "Guest Profile" {
			profileDir := filepath.Join(userDataDir, dirName)
			if ck := readMacProfileCookies(profileDir, key); ck != nil && ck["SESSDATA"] != "" {
				return ck
			}
		}
	}
	return nil
}

func readMacProfileCookies(profileDir string, key []byte) map[string]string {
	cookiePath := filepath.Join(profileDir, "Cookies")
	if _, err := os.Stat(cookiePath); err != nil {
		cookiePath = filepath.Join(profileDir, "Network", "Cookies")
		if _, err := os.Stat(cookiePath); err != nil {
			return nil
		}
	}

	tmpFile, err := os.CreateTemp("", "mac_bili_cookie_*.db")
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

	if cookies["SESSDATA"] != "" {
		return cookies
	}
	return nil
}

func decryptMacChromeCookie(encVal []byte, key []byte) string {
	if len(encVal) < 3 {
		return ""
	}
	if bytes.HasPrefix(encVal, []byte("v10")) || bytes.HasPrefix(encVal, []byte("v11")) {
		encVal = encVal[3:]
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return ""
	}

	if len(encVal)%aes.BlockSize != 0 {
		return ""
	}

	iv := bytes.Repeat([]byte(" "), aes.BlockSize)
	mode := cipher.NewCBCDecrypter(block, iv)
	dec := make([]byte, len(encVal))
	mode.CryptBlocks(dec, encVal)

	if len(dec) == 0 {
		return ""
	}
	padLen := int(dec[len(dec)-1])
	if padLen > 0 && padLen <= aes.BlockSize && padLen <= len(dec) {
		dec = dec[:len(dec)-padLen]
	}

	return string(dec)
}

func readPlatformBrowserCookies() (map[string]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	// 1. Google Chrome
	if ck := readMacChromiumUserData(filepath.Join(home, "Library/Application Support/Google/Chrome"), "Chrome Safe Storage"); ck != nil {
		return ck, nil
	}

	// 2. Microsoft Edge
	if ck := readMacChromiumUserData(filepath.Join(home, "Library/Application Support/Microsoft Edge"), "Microsoft Edge Safe Storage"); ck != nil {
		return ck, nil
	}

	// 3. Brave Browser
	if ck := readMacChromiumUserData(filepath.Join(home, "Library/Application Support/BraveSoftware/Brave-Browser"), "Brave Safe Storage"); ck != nil {
		return ck, nil
	}

	// 4. Arc Browser
	if ck := readMacChromiumUserData(filepath.Join(home, "Library/Application Support/Arc/User Data"), "Arc Safe Storage"); ck != nil {
		return ck, nil
	}

	// 5. Vivaldi
	if ck := readMacChromiumUserData(filepath.Join(home, "Library/Application Support/Vivaldi"), "Vivaldi Safe Storage"); ck != nil {
		return ck, nil
	}

	// 6. Chromium
	if ck := readMacChromiumUserData(filepath.Join(home, "Library/Application Support/Chromium"), "Chromium Safe Storage"); ck != nil {
		return ck, nil
	}

	// 7. Mozilla Firefox
	ffDir := filepath.Join(home, "Library/Application Support/Firefox/Profiles")
	if ffCk := readFirefoxCookies(ffDir); ffCk != nil && ffCk["SESSDATA"] != "" {
		return ffCk, nil
	}

	return nil, fmt.Errorf("未能从本机浏览器（Chrome/Edge/Brave/Firefox等）中提取到已登录的 B 站 Cookie，建议使用手机 B 站 App 扫码登录或手动填入 Cookie")
}
