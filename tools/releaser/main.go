package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"bilibili_downloader/tools/releaser/internal/publisher"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	wr "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed frontend/index.html frontend/app.js frontend/style.css
var embedded embed.FS
var defaultProject string

type App struct {
	ctx     context.Context
	manager *publisher.Manager
}

func projectDefault() string {
	if env := os.Getenv("BBDOWN_PUBLISHER_PROJECT"); env != "" {
		return env
	}
	if defaultProject != "" {
		return defaultProject
	}
	for _, initial := range []string{func() string { p, _ := os.Getwd(); return p }(), func() string { p, _ := os.Executable(); return filepath.Dir(p) }()} {
		for p := initial; p != filepath.Dir(p); p = filepath.Dir(p) {
			if _, e := os.Stat(filepath.Join(p, "scripts/publish_release.py")); e == nil {
				return p
			}
		}
	}
	return ""
}
func (a *App) GetState() publisher.State               { return a.manager.GetState() }
func (a *App) Refresh() publisher.State                { return a.manager.Refresh() }
func (a *App) SaveSettings(s publisher.Settings) error { return a.manager.Configure(s) }
func (a *App) Start(notes string, resume bool) error   { return a.manager.Start(notes, resume) }
func (a *App) Stop()                                   { a.manager.Stop() }
func (a *App) Select(version string) error             { return a.manager.Select(version) }
func (a *App) Login(channel string) error              { return a.manager.LoginBrowser(channel) }
func (a *App) LoginCloud(base, user, password string) error {
	return a.manager.LoginCloud(base, user, password)
}
func (a *App) LogoutCloud() error { return a.manager.LogoutCloud() }
func (a *App) ChooseProject() (string, error) {
	return wr.OpenDirectoryDialog(a.ctx, wr.OpenDialogOptions{Title: "选择 BBDown Pro 项目目录", DefaultDirectory: a.manager.GetState().Settings.Project})
}
func (a *App) Open(target string) error {
	link, e := a.manager.Link(target)
	if e != nil {
		return e
	}
	if strings.HasPrefix(link, "https://") {
		wr.BrowserOpenURL(a.ctx, link)
		return nil
	}
	if _, e := os.Stat(link); e != nil {
		return errors.New("文件尚未生成，完成对应步骤后再打开")
	}
	return exec.Command("/usr/bin/open", link).Run()
}
func main() {
	config, _ := os.UserConfigDir()
	if override := os.Getenv("BBDOWN_PUBLISHER_SETTINGS"); override != "" {
		config = override
	} else {
		config = filepath.Join(config, "bbdown-publisher", "settings.json")
	}
	a := &App{manager: publisher.New(projectDefault(), config, publisher.Keychain{})}
	assets, _ := fs.Sub(embedded, "frontend")
	err := wails.Run(&options.App{
		Title: "BBDown 发布器", Width: 1180, Height: 850, MinWidth: 980, MinHeight: 720,
		BackgroundColour: options.NewRGB(13, 19, 26), AssetServer: &assetserver.Options{Assets: assets},
		OnStartup: func(ctx context.Context) { a.ctx = ctx },
		OnBeforeClose: func(ctx context.Context) bool {
			if a.manager.GetState().Busy {
				wr.WindowHide(ctx)
				return true
			}
			return false
		},
		OnShutdown:         func(context.Context) { a.manager.Stop() },
		SingleInstanceLock: &options.SingleInstanceLock{UniqueId: "com.bbdown.publisher", OnSecondInstanceLaunch: func(options.SecondInstanceData) { wr.WindowShow(a.ctx); wr.WindowUnminimise(a.ctx) }},
		Bind:               []interface{}{a}, Mac: &mac.Options{TitleBar: mac.TitleBarHiddenInset(), Appearance: mac.NSAppearanceNameDarkAqua,
			About: &mac.AboutInfo{Title: "BBDown 发布器", Message: "可恢复的双平台发布工具 · 1.0.0"}},
	})
	if err != nil {
		println(err.Error())
	}
}
