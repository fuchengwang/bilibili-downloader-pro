package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"

	"bilibili_downloader/pkg/config"
	"bilibili_downloader/pkg/update"
	"bilibili_downloader/pkg/updater"
	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) initializeUpdates() bool {
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		a.updateInitError = "无法读取软件位置，暂时无法更新"
		return false
	}
	directory := filepath.Join(config.GetConfigDir(), "updates")
	client, err := update.New(update.Config{ServerURL: serviceURL, AppID: "bbdown-pro", CurrentVersion: appVersion()})
	if err != nil {
		a.updateInitError = "更新服务初始化失败，请重启后重试"
		log.Printf("initialize update client: %v", err)
		return false
	}
	handled, installErr := updater.LaunchPending(directory, executable, appVersion())
	if handled {
		return true
	}
	if installErr != nil {
		log.Printf("pending update: %v", installErr)
	}
	a.updates, err = updater.New(updater.Config{Directory: directory, Version: appVersion(), Executable: executable, Client: client})
	if err != nil {
		a.updateInitError = "无法保存更新信息，请确认用户目录可写"
		log.Printf("initialize updater: %v", err)
	}
	return false
}

func (a *App) GetUpdateState() updater.State {
	if a.updates == nil {
		return updater.State{CurrentVersion: appVersion(), Phase: "idle", Error: a.updateInitError}
	}
	return a.updates.Snapshot()
}
func (a *App) updateContext() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}
func (a *App) requireUpdater() error {
	if a.updates == nil {
		return errors.New("更新模块暂不可用，请重启后重试")
	}
	return nil
}
func (a *App) SetAutomaticUpdateCheck(enabled bool) error {
	if err := a.requireUpdater(); err != nil {
		return err
	}
	return a.updates.SetAutoCheck(enabled)
}
func (a *App) CheckForUpdates() error {
	if err := a.requireUpdater(); err != nil {
		return err
	}
	return a.updates.Check(a.updateContext())
}
func (a *App) DownloadUpdate() error {
	if err := a.requireUpdater(); err != nil {
		return err
	}
	return a.updates.Download(a.updateContext())
}
func (a *App) RetryUpdateDownload() error {
	if err := a.requireUpdater(); err != nil {
		return err
	}
	return a.updates.RetryDownload(a.updateContext())
}
func (a *App) PauseUpdateDownload() {
	if a.updates != nil {
		a.updates.Pause()
	}
}
func (a *App) RestartForUpdate() error {
	if err := a.requireUpdater(); err != nil {
		return err
	}
	if a.updates.Snapshot().Phase != "ready" {
		return errors.New("请先完成更新下载")
	}
	if mgr := a.currentDownloadManager(); mgr != nil {
		ctx, cancel := context.WithTimeout(a.updateContext(), 15*time.Second)
		defer cancel()
		if err := mgr.PrepareForRestart(ctx); err != nil {
			return err
		}
	}
	if err := a.updates.LaunchInstall(); err != nil {
		return err
	}
	// Let the RPC return before closing the webview. The helper waits for the
	// actual process exit before changing any installed files.
	go func() { time.Sleep(300 * time.Millisecond); wailsRuntime.Quit(a.ctx) }()
	return nil
}

func (a *App) shutdown(context.Context) {
	if a.updates != nil {
		a.updates.Close()
	}
}
