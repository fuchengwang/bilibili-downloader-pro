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
	windowsTheme := windows.Dark
	background := &options.RGBA{R: 13, G: 13, B: 15, A: 255}
	darkTitleBar := windows.RGB(24, 24, 29)
	darkTitleBarInactive := windows.RGB(20, 20, 25)
	darkBorder := windows.RGB(44, 44, 52)
	switch app.cfgMgr.Get().Theme {
	case "midnight":
		background = &options.RGBA{R: 12, G: 14, B: 20, A: 255}
		darkTitleBar = windows.RGB(24, 29, 41)
		darkTitleBarInactive = windows.RGB(20, 24, 34)
		darkBorder = windows.RGB(42, 51, 68)
	case "light":
		appearance = mac.NSAppearanceNameAqua
		windowsTheme = windows.Light
		background = &options.RGBA{R: 245, G: 245, B: 247, A: 255}
	case "system":
		appearance = mac.DefaultAppearance
		windowsTheme = windows.SystemDefault
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
			Assets:     assets,
			Middleware: startupThemeMiddleware(func() string { return app.cfgMgr.Get().Theme }, app.GetCachedUserInfo),
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		OnDomReady: app.domReady,
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
			Theme:                windowsTheme,
			CustomTheme: &windows.ThemeSettings{
				DarkModeTitleBar:           darkTitleBar,
				DarkModeTitleBarInactive:   darkTitleBarInactive,
				DarkModeTitleText:          windows.RGB(244, 244, 246),
				DarkModeTitleTextInactive:  windows.RGB(161, 161, 170),
				DarkModeBorder:             darkBorder,
				LightModeTitleBar:          windows.RGB(232, 232, 237),
				LightModeTitleBarInactive:  windows.RGB(238, 238, 242),
				LightModeTitleText:         windows.RGB(24, 24, 27),
				LightModeTitleTextInactive: windows.RGB(113, 113, 122),
				LightModeBorder:            windows.RGB(214, 214, 222),
			},
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
