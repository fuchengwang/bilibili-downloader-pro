package main

import (
	"context"
	"fmt"

	"bilibili_downloader/pkg/bilibili"
	"bilibili_downloader/pkg/config"
	"bilibili_downloader/pkg/downloader"
	"bilibili_downloader/pkg/utils"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App struct
type App struct {
	ctx        context.Context
	biliClient *bilibili.Client
	downMgr    *downloader.DownloadManager
	cfgMgr     *config.ConfigManager
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{
		biliClient: bilibili.GetDefaultClient(),
		downMgr:    downloader.GetManager(),
		cfgMgr:     config.GetManager(),
	}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	// 注册下载管理器的状态变更回调，通过 Wails 事件广播至前端
	a.downMgr.SetCallback(func(t *downloader.DownloadTask) {
		wailsRuntime.EventsEmit(a.ctx, "task:progress", t)
	})
}

// domReady is called after front-end resources are completely loaded
func (a *App) domReady(ctx context.Context) {
	wailsRuntime.WindowShow(ctx)
}

// ParseURL 解析用户输入的链接或 ID，返回视频及全部分P详情
func (a *App) ParseURL(input string) (*bilibili.VideoDetail, error) {
	target, err := a.biliClient.ParseInput(a.ctx, input)
	if err != nil {
		return nil, err
	}
	return a.biliClient.FetchVideoDetail(a.ctx, target)
}

// GetAvailableQualities 获取指定分P在当前登录状态下的全部可用清晰度
func (a *App) GetAvailableQualities(bvid string, aid, cid, epid int64, isBangumi bool) ([]bilibili.QualityOption, error) {
	return a.biliClient.GetAvailableQualities(a.ctx, bvid, aid, cid, epid, isBangumi)
}

// AddDownloadTasks 添加一集或多集下载任务
func (a *App) AddDownloadTasks(req downloader.DownloadRequest) ([]*downloader.DownloadTask, error) {
	// 先获取视频完整信息以匹配选中的 CID
	var targetType bilibili.TargetType = bilibili.TargetNormal
	if req.IsBangumi {
		targetType = bilibili.TargetBangumi
	}
	detail, err := a.biliClient.FetchVideoDetail(a.ctx, &bilibili.ParsedTarget{
		Type: targetType,
		BVID: req.BVID,
		AID:  fmt.Sprintf("%d", req.AID),
	})
	if err != nil {
		return nil, fmt.Errorf("无法获取集数元数据: %w", err)
	}

	cidMap := make(map[int64]bool)
	for _, cid := range req.Episodes {
		cidMap[cid] = true
	}

	var added []*downloader.DownloadTask
	for _, ep := range detail.Episodes {
		if cidMap[ep.CID] {
			t, err := a.downMgr.AddDownloadTask(&req, &ep)
			if err == nil && t != nil {
				added = append(added, t)
			}
		}
	}

	if len(added) == 0 {
		return nil, fmt.Errorf("未选择任何有效集数")
	}

	return added, nil
}

// PauseTask 暂停单个任务
func (a *App) PauseTask(id string) error {
	return a.downMgr.PauseTask(id)
}

// ResumeTask 继续单个任务
func (a *App) ResumeTask(id string) error {
	return a.downMgr.ResumeTask(id)
}

// CancelTask 取消单个任务
func (a *App) CancelTask(id string) error {
	return a.downMgr.CancelTask(id)
}

// DeleteTask 删除任务记录
func (a *App) DeleteTask(id string, deleteFile bool) error {
	return a.downMgr.DeleteTask(id, deleteFile)
}

// PauseAllTasks 暂停全部任务
func (a *App) PauseAllTasks() error {
	a.downMgr.PauseAll()
	return nil
}

// ResumeAllTasks 继续全部任务
func (a *App) ResumeAllTasks() error {
	a.downMgr.ResumeAll()
	return nil
}

// ClearCompletedTasks 清理已完成任务列表
func (a *App) ClearCompletedTasks() error {
	a.downMgr.ClearCompleted()
	return nil
}

// GetTasks 获取全部任务列表
func (a *App) GetTasks() []*downloader.DownloadTask {
	return a.downMgr.GetTasks()
}

// GetSettings 获取设置
func (a *App) GetSettings() config.Settings {
	return a.cfgMgr.Get()
}

// SaveSettings 保存设置
func (a *App) SaveSettings(s config.Settings) error {
	return a.cfgMgr.Save(s)
}

// SelectDirectory 弹出系统原生文件夹选择对话框
func (a *App) SelectDirectory() (string, error) {
	current := a.cfgMgr.Get().DownloadDir
	res, err := wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		DefaultDirectory: current,
		Title:            "选择下载保存目录",
	})
	if err != nil {
		return "", err
	}
	if res != "" {
		s := a.cfgMgr.Get()
		s.DownloadDir = res
		_ = a.cfgMgr.Save(s)
	}
	return res, nil
}

// OpenDirectory 在系统资源管理器中打开指定目录或文件所在位置
func (a *App) OpenDirectory(path string) error {
	if path == "" {
		path = a.cfgMgr.Get().DownloadDir
	}
	return utils.OpenDirectory(path)
}

// OpenFile 使用系统默认播放器打开已下载的视频文件
func (a *App) OpenFile(path string) error {
	if path == "" {
		return fmt.Errorf("文件路径为空")
	}
	return utils.OpenFile(path)
}

// OpenNativeBrowserLogin 调起底层系统原生独立浏览器窗口进行登录（避开 iframe 隔离）
func (a *App) OpenNativeBrowserLogin() error {
	return a.biliClient.OpenNativeBrowserLogin(a.ctx)
}

// GenerateQRCode 获取二维码登录信息
func (a *App) GenerateQRCode() (*bilibili.QRCodeInfo, error) {
	return a.biliClient.GenerateQRCode(a.ctx)
}

// PollQRCode 轮询二维码扫码状态
func (a *App) PollQRCode(key string) (*bilibili.QRStatus, error) {
	return a.biliClient.PollQRCode(a.ctx, key)
}

// GetUserInfo 获取当前登录用户信息
func (a *App) GetUserInfo() (*bilibili.UserInfo, error) {
	return a.biliClient.GetUserInfo(a.ctx)
}

// Logout 退出登录
func (a *App) Logout() error {
	return a.biliClient.ClearCookies()
}

// SaveRawCookie 用户手动保存 Cookie
func (a *App) SaveRawCookie(cookieStr string) error {
	return a.biliClient.ParseAndSaveRawCookie(cookieStr)
}

// CheckFFmpeg 检查内置合成引擎状态 (纯 Go 原生极速合成，100% 始终就绪)
func (a *App) CheckFFmpeg() (map[string]any, error) {
	return map[string]any{
		"ready":   true,
		"path":    "内置 Go 原生合成引擎",
		"info":    "Pure Go Built-in Muxer (Zero Dependency, Native Fast Muxing)",
		"error":   "",
		"autoDir": "",
	}, nil
}

// ReadClipboard 读取系统剪贴板内容
func (a *App) ReadClipboard() (string, error) {
	return wailsRuntime.ClipboardGetText(a.ctx)
}
