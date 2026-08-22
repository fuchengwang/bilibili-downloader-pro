//go:build !windows && !darwin

package bilibili

import (
	"fmt"
	"os"
	"path/filepath"
)

func readPlatformBrowserCookies() (map[string]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	ffDir := filepath.Join(home, ".mozilla/firefox")
	if ffCk := readFirefoxCookies(ffDir); ffCk != nil && ffCk["SESSDATA"] != "" {
		return ffCk, nil
	}

	return nil, fmt.Errorf("当前系统暂不支持浏览器自动提取，请使用扫码登录或手动填入 Cookie")
}
