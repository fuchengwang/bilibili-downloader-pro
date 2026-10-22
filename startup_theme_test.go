//go:build !dev

package main

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	wailsassets "github.com/wailsapp/wails/v2/pkg/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

type startupTestRuntime struct{}

func (startupTestRuntime) RuntimeDesktopJS() []byte { return []byte("/* runtime */") }
func (startupTestRuntime) DesktopIPC() []byte       { return []byte("/* ipc */") }
func (startupTestRuntime) WebsocketIPC() []byte     { return nil }

func TestStartupHTMLUsesSavedThemeAndKeepsWailsRuntime(t *testing.T) {
	theme := "light"
	server, err := wailsassets.NewAssetServer("", assetserver.Options{
		Assets: assets, Middleware: startupThemeMiddleware(func() string { return theme }),
	}, false, nil, startupTestRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	for _, preference := range []string{"light", "dark", "system", `invalid"value`} {
		theme = preference
		want := preference
		if want != "light" && want != "system" {
			want = "dark"
		}
		for _, path := range []string{"/", "/index.html"} {
			response := httptest.NewRecorder()
			server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			body := response.Body.String()
			if response.Code != http.StatusOK || !strings.Contains(body, `data-startup-theme="`+want+`"`) || !strings.Contains(body, `data-theme="`+want+`"`) {
				t.Fatalf("initial HTML did not use saved theme %q", preference)
			}
			if !strings.Contains(body, "/wails/runtime.js") || !strings.Contains(body, "/wails/ipc.js") {
				t.Fatal("theme injection broke Wails runtime injection")
			}
		}
	}
	// Normal asset requests must keep using Wails' existing asset handler.
	files, err := fs.Glob(assets, "frontend/dist/assets/*.css")
	if err != nil || len(files) == 0 {
		t.Fatalf("missing built stylesheet: %v", err)
	}
	want, err := assets.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/"+strings.TrimPrefix(files[0], "frontend/dist/"), nil))
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), want) {
		t.Fatal("theme injection changed stylesheet delivery")
	}
}
