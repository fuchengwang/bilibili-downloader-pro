//go:build dev

package main

import (
	"bilibili_downloader/pkg/bilibili"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// Development continues to use Vite's live HTML and hot reload.
func startupThemeMiddleware(func() string, ...func() *bilibili.UserInfo) assetserver.Middleware {
	return nil
}
