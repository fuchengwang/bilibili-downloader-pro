package main

import (
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"bilibili_downloader/pkg/config"
	"bilibili_downloader/pkg/instance"
	"bilibili_downloader/pkg/license"
	"bilibili_downloader/pkg/updater"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--bbdown-version" {
		fmt.Println(appVersion())
		return
	}
	if handled, err := updater.HandleHelperArgs(os.Args[1:]); handled {
		if err != nil {
			log.Printf("install update: %v", err)
		}
		return
	}

	licenseClient, err := license.New(license.Config{
		ServerURL:            serviceURL,
		AppID:                "bbdown-pro",
		AppName:              "BBDown Pro",
		ClientVersion:        appVersion(),
		Timeout:              10 * time.Second,
		OfflineGraceDays:     30,
		AutoVerifyInterval:   24 * time.Hour,
		RefundProtectionDays: 15,
	})
	if err != nil {
		log.Fatalf("初始化授权服务失败: %v", err)
	}
	app := NewApp(licenseClient)
	if normalApplication {
		if handled, err := updater.ContinueInstallation(filepath.Join(config.GetConfigDir(), "updates"), appVersion()); handled {
			if err != nil {
				log.Printf("continue update: %v", err)
			}
			return
		}

		// Ensure single instance
		_ = instance.SetupSingleInstance(func() {
			app.ShowMainWindow()
		})

		if app.initializeUpdates() {
			return
		}

	}

	appearance := mac.NSAppearanceNameDarkAqua
	background := &options.RGBA{R: 12, G: 14, B: 20, A: 255}
	switch app.cfgMgr.Get().Theme {
	case "light":
		appearance = mac.NSAppearanceNameAqua
		background = &options.RGBA{R: 244, G: 246, B: 250, A: 255}
	case "system":
		appearance = mac.DefaultAppearance
	}
	err = wails.Run(&options.App{
		Title:            "BBDown Pro",
		Width:            1140,
		Height:           760,
		MinWidth:         980,
		MinHeight:        640,
		StartHidden:      true,
		BackgroundColour: background,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		OnDomReady: app.domReady, // 前端 DOM 彻底就绪后再展示窗口，彻底消灭白屏/灰屏闪烁
		Bind: []interface{}{
			app,
		},
		Mac: &mac.Options{
			TitleBar:             mac.TitleBarHiddenInset(),
			Appearance:           appearance,
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			About: &mac.AboutInfo{
				Title:   "BBDown Pro",
				Message: "高品质、极速、现代的 Bilibili 桌面下载工具",
			},
			Preferences: &mac.Preferences{
				ApplicationNameForUserAgent: "Version/18.0 Safari/605.1.15 Chrome/133.0.0.0",
			},
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			BackdropType:         windows.Mica,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
