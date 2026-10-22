//go:build !dev

package main

import (
	"bytes"
	"fmt"
	"net/http"

	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

// Use the same saved preference for the initial HTML and the native window.
func startupThemeMiddleware(getTheme func() string) assetserver.Middleware {
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
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(index)
		})
	}
}
