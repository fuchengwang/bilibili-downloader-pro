package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"bilibili_downloader/pkg/bilibili"
	"bilibili_downloader/pkg/config"
	"bilibili_downloader/pkg/downloader"
	"bilibili_downloader/pkg/license"
	"bilibili_downloader/pkg/utils"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type licenseChecker interface {
	CheckStatus(context.Context, bool) (*license.LicenseStatus, error)
	Activate(context.Context, string) (*license.LicenseStatus, error)
	Deactivate(context.Context) error
}

type LicenseResult struct {
	Success bool                   `json:"success"`
	Message string                 `json:"message"`
	Data    *license.LicenseStatus `json:"data,omitempty"`
}

// App struct
type App struct {
	ctx        context.Context
	biliClient *bilibili.Client
	downMgr    *downloader.DownloadManager
	cfgMgr     *config.ConfigManager
	license    licenseChecker
	downMu     sync.Mutex
}

// NewApp creates a new App application struct
func NewApp(licenseClient licenseChecker) *App {
	return &App{
		biliClient: bilibili.GetDefaultClient(),
		cfgMgr:     config.GetManager(),
		license:    licenseClient,
	}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	status, err := a.license.CheckStatus(ctx, false)
	if err == nil && status != nil && status.IsActivated {
		a.ensureDownloadManager()
	}

	go func() {
		// 核心安全防护：启动 4 秒兜底定时器，若系统因 WebView2 初始化缓慢或环境异常未触发 domReady，
		// 强制展现窗口，彻底杜绝 Windows / macOS 下软件常驻后台但窗口永远隐形的假死现象
		time.Sleep(4 * time.Second)
		if a.ctx != nil {
			wailsRuntime.WindowShow(a.ctx)
		}
	}()
}

func (a *App) ensureDownloadManager() *downloader.DownloadManager {
	a.downMu.Lock()
	defer a.downMu.Unlock()
	if a.downMgr != nil {
		return a.downMgr
	}
	a.downMgr = downloader.GetManager()
	// 注册下载管理器的状态变更回调，通过 Wails 事件广播至前端
	a.downMgr.SetCallback(func(t *downloader.DownloadTask) {
		wailsRuntime.EventsEmit(a.ctx, "task:progress", t)
		wailsRuntime.EventsEmit(a.ctx, "task:update", t)
		if t.Status == downloader.StatusCompleted {
			wailsRuntime.EventsEmit(a.ctx, "task:completed", t)
		} else if t.Status == downloader.StatusError {
			wailsRuntime.EventsEmit(a.ctx, "task:error", t)
		}
	})
	return a.downMgr
}

func (a *App) currentDownloadManager() *downloader.DownloadManager {
	a.downMu.Lock()
	defer a.downMu.Unlock()
	return a.downMgr
}

func (a *App) requireLicense() error {
	status, err := a.license.CheckStatus(context.Background(), false)
	if err != nil || status == nil || !status.IsActivated {
		return fmt.Errorf("BBDown Pro 尚未激活")
	}
	a.ensureDownloadManager()
	return nil
}

// CheckLicense 供启动页检查本机授权，本地有效时不会阻塞等待网络。
func (a *App) CheckLicense(forceOnline bool) LicenseResult {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status, err := a.license.CheckStatus(ctx, forceOnline)
	if err != nil || status == nil || !status.IsActivated {
		message := "请输入激活码后继续使用"
		if status != nil && status.Message != "" {
			message = status.Message
		} else if err != nil {
			message = err.Error()
		}
		return LicenseResult{Message: message, Data: status}
	}
	a.ensureDownloadManager()
	return LicenseResult{Success: true, Message: status.Message, Data: status}
}

// ActivateLicense 首次联网绑定当前设备，成功后立即开放下载功能。
func (a *App) ActivateLicense(key string) LicenseResult {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	status, err := a.license.Activate(ctx, key)
	if err != nil {
		return LicenseResult{Message: err.Error()}
	}
	a.ensureDownloadManager()
	return LicenseResult{Success: true, Message: "激活成功", Data: status}
}

// DeactivateLicense 联网释放本机名额，成功后清除本地授权并暂停当前下载。
func (a *App) DeactivateLicense() LicenseResult {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := a.license.Deactivate(ctx); err != nil {
		return LicenseResult{Message: err.Error()}
	}
	if downMgr := a.currentDownloadManager(); downMgr != nil {
		downMgr.PauseAll()
	}
	return LicenseResult{Success: true, Message: "本机已解绑"}
}

// domReady is called after front-end resources are completely loaded
func (a *App) domReady(ctx context.Context) {
	wailsRuntime.WindowShow(ctx)
}

// ShowMainWindow brings the window to the foreground and unminimizes it
func (a *App) ShowMainWindow() {
	if a.ctx != nil {
		wailsRuntime.WindowShow(a.ctx)
		wailsRuntime.WindowUnminimise(a.ctx)
		wailsRuntime.EventsEmit(a.ctx, "app:wakeup")
	}
}

// ParseURL 解析用户输入的链接或 ID，返回视频及全部分P详情
func (a *App) ParseURL(input string) (*bilibili.VideoDetail, error) {
	if err := a.requireLicense(); err != nil {
		return nil, err
	}
	target, err := a.biliClient.ParseInput(a.ctx, input)
	if err != nil {
		return nil, err
	}
	return a.biliClient.FetchVideoDetail(a.ctx, target)
}

// GetAvailableQualities 获取指定分P在当前登录状态下的全部可用清晰度
func (a *App) GetAvailableQualities(bvid string, aid, cid, epid int64, isBangumi, isCheese bool) ([]bilibili.QualityOption, error) {
	if err := a.requireLicense(); err != nil {
		return nil, err
	}
	return a.biliClient.GetAvailableQualities(a.ctx, bvid, aid, cid, epid, isBangumi, isCheese)
}

// AddDownloadTasks 添加一集或多集下载任务
func (a *App) AddDownloadTasks(req downloader.DownloadRequest) ([]*downloader.DownloadTask, error) {
	if err := a.requireLicense(); err != nil {
		return nil, err
	}
	downMgr := a.currentDownloadManager()
	// 先获取视频完整信息以匹配选中的 CID
	var targetType bilibili.TargetType = bilibili.TargetNormal
	var epidStr, ssidStr string
	if req.IsBangumi || req.IsCheese {
		targetType = bilibili.TargetBangumi
		if req.IsCheese {
			targetType = bilibili.TargetCheese
		}
		if req.EPID > 0 {
			epidStr = fmt.Sprintf("%d", req.EPID)
		}
		if req.SSID > 0 {
			ssidStr = fmt.Sprintf("%d", req.SSID)
		}
	}
	detail, err := a.biliClient.FetchVideoDetail(a.ctx, &bilibili.ParsedTarget{
		Type: targetType,
		BVID: req.BVID,
		AID:  fmt.Sprintf("%d", req.AID),
		EPID: epidStr,
		SSID: ssidStr,
	})
	if err != nil {
		return nil, fmt.Errorf("无法获取集数元数据: %w", err)
	}
	req.IsCheese = detail.Type == bilibili.TargetCheese
	req.IsBangumi = detail.Type == bilibili.TargetBangumi
	if req.IsCheese {
		req.SSID = detail.SeasonID
	}

	cidMap := make(map[int64]bool)
	for _, cid := range req.Episodes {
		cidMap[cid] = true
	}

	var added []*downloader.DownloadTask
	var firstAddErr error
	for _, ep := range detail.Episodes {
		if cidMap[ep.CID] {
			t, addErr := downMgr.AddDownloadTask(&req, &ep)
			if addErr != nil && firstAddErr == nil {
				firstAddErr = addErr
			}
			if t != nil {
				// 从管理器获取锁内快照，避免把后台 worker 仍会修改的指针直接交给 Wails 序列化。
				for _, snapshot := range downMgr.GetTasks() {
					if snapshot.ID == t.ID {
						added = append(added, snapshot)
						break
					}
				}
			}
		}
	}

	if firstAddErr != nil {
		return added, firstAddErr
	}
	if len(added) == 0 {
		return nil, fmt.Errorf("未选择任何有效集数")
	}

	return added, nil
}

// PauseTask 暂停单个任务
func (a *App) PauseTask(id string) error {
	downMgr := a.currentDownloadManager()
	if downMgr == nil {
		return nil
	}
	return downMgr.PauseTask(id)
}

// ResumeTask 继续单个任务
func (a *App) ResumeTask(id string) error {
	if err := a.requireLicense(); err != nil {
		return err
	}
	return a.currentDownloadManager().ResumeTask(id)
}

// CancelTask 取消单个任务
func (a *App) CancelTask(id string) error {
	downMgr := a.currentDownloadManager()
	if downMgr == nil {
		return nil
	}
	return downMgr.CancelTask(id)
}

// DeleteTask 删除任务记录
func (a *App) DeleteTask(id string, deleteFile bool) error {
	downMgr := a.currentDownloadManager()
	if downMgr == nil {
		return nil
	}
	return downMgr.DeleteTask(id, deleteFile)
}

// PauseAllTasks 暂停全部任务
func (a *App) PauseAllTasks() error {
	downMgr := a.currentDownloadManager()
	if downMgr == nil {
		return nil
	}
	return downMgr.PauseAll()
}

// ResumeAllTasks 继续全部任务
func (a *App) ResumeAllTasks() error {
	if err := a.requireLicense(); err != nil {
		return err
	}
	return a.currentDownloadManager().ResumeAll()
}

// ClearCompletedTasks 清理已完成任务列表 (可选择是否同时删除本地文件)
func (a *App) ClearCompletedTasks(deleteFile bool) error {
	downMgr := a.currentDownloadManager()
	if downMgr == nil {
		return nil
	}
	return downMgr.ClearCompleted(deleteFile)
}

// GetTasks 获取全部任务列表
func (a *App) GetTasks() []*downloader.DownloadTask {
	downMgr := a.currentDownloadManager()
	if downMgr == nil {
		return []*downloader.DownloadTask{}
	}
	return downMgr.GetTasks()
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
	if fi, err := os.Stat(current); err != nil || !fi.IsDir() {
		current = config.DefaultDownloadDir()
	}
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
		if err := a.cfgMgr.Save(s); err != nil {
			return "", fmt.Errorf("保存下载目录失败: %w", err)
		}
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

// CheckFileExists 检查指定本地路径文件是否存在且非目录
func (a *App) CheckFileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// OpenFile 使用系统默认播放器打开已下载的视频文件
func (a *App) OpenFile(path string) error {
	if path == "" {
		return fmt.Errorf("文件路径为空")
	}
	if !a.CheckFileExists(path) {
		return fmt.Errorf("FILE_NOT_FOUND")
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

// CheckFFmpeg 检查音视频合成引擎状态 (纯 Go 原生复用引擎，零外部依赖)
func (a *App) CheckFFmpeg() (map[string]any, error) {
	return map[string]any{
		"ready":   true,
		"path":    "纯 Go 原生复用引擎 (零外部依赖)",
		"info":    "Pure Go Built-in Muxer (fMP4 / MP4 / FLAC)",
		"error":   "",
		"autoDir": a.cfgMgr.GetBinDir(),
	}, nil
}

// ReadClipboard 读取系统剪贴板内容
func (a *App) ReadClipboard() (string, error) {
	return wailsRuntime.ClipboardGetText(a.ctx)
}
