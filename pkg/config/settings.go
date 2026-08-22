package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// Settings 存储用户全局偏好设置
type Settings struct {
	DownloadDir      string `json:"downloadDir"`      // 下载存储目录
	DefaultQuality   string `json:"defaultQuality"`   // 默认清晰度: "highest", "127", "120", "116", "80", "64", "32"
	DefaultCodec     string `json:"defaultCodec"`     // 默认编码: "auto", "AVC", "HEVC", "AV1"
	MaxConcurrent    int    `json:"maxConcurrent"`    // 最大同时下载任务数
	ThreadsPerTask   int    `json:"threadsPerTask"`   // 单任务并发分块数
	AutoMerge        bool   `json:"autoMerge"`        // 是否使用 FFmpeg 自动合成音视频
	DeleteTempFiles  bool   `json:"deleteTempFiles"`  // 合成后是否自动删除临时分块文件
	FFmpegPath       string `json:"ffmpegPath"`       // 自定义或检测到的 FFmpeg 路径
	AutoClipboard    bool   `json:"autoClipboard"`    // 是否自动检测剪贴板 B 站链接
	FileNameTemplate string `json:"fileNameTemplate"` // 文件名命名格式: "{title} - {part}"
	Theme            string `json:"theme"`            // 主题: "dark", "light", "system"
}

// ConfigManager 负责配置的持久化与线程安全读取
type ConfigManager struct {
	mu       sync.RWMutex
	dir      string
	settings Settings
}

var (
	instance *ConfigManager
	once     sync.Once
)

// GetConfigDir 获取应用数据存储目录
func GetConfigDir() string {
	var baseDir string
	if cdir, err := os.UserConfigDir(); err == nil {
		baseDir = cdir
	} else if hdir, err := os.UserHomeDir(); err == nil {
		baseDir = filepath.Join(hdir, ".config")
	} else {
		baseDir = "."
	}
	appDir := filepath.Join(baseDir, "bilibili-downloader-pro")
	_ = os.MkdirAll(appDir, 0755)
	return appDir
}

// DefaultDownloadDir 获取默认下载目录
func DefaultDownloadDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, "Downloads", "Bilibili")
		_ = os.MkdirAll(dir, 0755)
		return dir
	}
	return "./downloads"
}

// DetectFFmpeg 自动在常用路径和系统 PATH 中探测可用 ffmpeg
func DetectFFmpeg() string {
	// 1. 尝试常见的标准路径 (针对 macOS Homebrew / MacPorts / Windows)
	candidates := []string{
		"/opt/homebrew/bin/ffmpeg",
		"/usr/local/bin/ffmpeg",
		"/usr/bin/ffmpeg",
		"C:\\ffmpeg\\bin\\ffmpeg.exe",
		"C:\\Program Files\\ffmpeg\\bin\\ffmpeg.exe",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			// 简单验证是否可执行
			if out, err := exec.Command(p, "-version").Output(); err == nil && len(out) > 0 {
				return p
			}
		}
	}

	// 2. 检查系统 PATH
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		if out, err := exec.Command(p, "-version").Output(); err == nil && len(out) > 0 {
			return p
		}
	}

	if runtime.GOOS == "windows" {
		if p, err := exec.LookPath("ffmpeg.exe"); err == nil {
			return p
		}
	}

	return ""
}

// GetManager 获取单例配置管理器
func GetManager() *ConfigManager {
	once.Do(func() {
		cfgDir := GetConfigDir()
		mgr := &ConfigManager{
			dir: cfgDir,
			settings: Settings{
				DownloadDir:      DefaultDownloadDir(),
				DefaultQuality:   "highest",
				DefaultCodec:     "auto",
				MaxConcurrent:    3,
				ThreadsPerTask:   4,
				AutoMerge:        true,
				DeleteTempFiles:  true,
				FFmpegPath:       DetectFFmpeg(),
				AutoClipboard:    false,
				FileNameTemplate: "{title} - {part}",
				Theme:            "dark",
			},
		}
		_ = mgr.load()
		instance = mgr
	})
	return instance
}

func (m *ConfigManager) load() error {
	filePath := filepath.Join(m.dir, "settings.json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	// 补充默认值
	if s.DownloadDir == "" {
		s.DownloadDir = DefaultDownloadDir()
	}
	if s.DefaultQuality == "" {
		s.DefaultQuality = "highest"
	}
	if s.DefaultCodec == "" {
		s.DefaultCodec = "auto"
	}
	if s.MaxConcurrent <= 0 {
		s.MaxConcurrent = 3
	}
	if s.ThreadsPerTask <= 0 {
		s.ThreadsPerTask = 8
	}
	if s.FFmpegPath == "" {
		s.FFmpegPath = DetectFFmpeg()
	}
	if s.FileNameTemplate == "" {
		s.FileNameTemplate = "{title} - {part}"
	}
	if s.Theme == "" {
		s.Theme = "dark"
	}

	m.settings = s
	return nil
}

// Get 获取当前设置的拷贝
func (m *ConfigManager) Get() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings
}

// Save 保存设置
func (m *ConfigManager) Save(s Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 确保目录存在
	if s.DownloadDir != "" {
		_ = os.MkdirAll(s.DownloadDir, 0755)
	}

	m.settings = s
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	filePath := filepath.Join(m.dir, "settings.json")
	return os.WriteFile(filePath, data, 0644)
}

// GetCookiesPath 获取 Cookie 存储路径
func (m *ConfigManager) GetCookiesPath() string {
	return filepath.Join(m.dir, "cookies.json")
}

// GetTasksPath 获取任务历史持久化路径
func (m *ConfigManager) GetTasksPath() string {
	return filepath.Join(m.dir, "tasks.json")
}
