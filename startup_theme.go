//go:build !dev

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"bilibili_downloader/pkg/bilibili"

	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// Use the same saved preference for the initial HTML and the native window.
func startupThemeMiddleware(getTheme func() string, getAccount ...func() *bilibili.UserInfo) assetserver.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || (r.URL.Path != "/" && r.URL.Path != "/index.html") {
				next.ServeHTTP(w, r)
				return
			}
			index, err := assets.ReadFile("frontend/dist/index.html")
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}
			theme := getTheme()
			if theme != "light" && theme != "system" {
				theme = "dark"
			}
			root := fmt.Sprintf(`<html data-startup-theme="%s" data-theme="%s"`, theme, theme)
			index = bytes.Replace(index, []byte("<html"), []byte(root), 1)
			if len(getAccount) > 0 {
				// encoding/json escapes HTML characters, including any closing
				// script tag in a username. No credentials enter the document.
				profile, err := json.Marshal(getAccount[0]())
				if err == nil {
					bootstrap := append([]byte(`<script id="bbdown-startup-account" type="application/json">`), profile...)
					bootstrap = append(bootstrap, []byte("</script></head>")...)
					index = bytes.Replace(index, []byte("</head>"), bootstrap, 1)
				}
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(index)
		})
	}
}
