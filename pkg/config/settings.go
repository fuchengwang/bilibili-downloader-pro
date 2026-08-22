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

// DetectFFmpeg 自动在执行程序同级目录、内置目录、常用路径和系统 PATH 中探测可用 ffmpeg
func DetectFFmpeg() string {
	var candidates []string

	// 0. 优先检测当前运行程序同级目录及 bin 子目录
	if execPath, err := os.Executable(); err == nil {
		execDir := filepath.Dir(execPath)
		candidates = append(candidates,
			filepath.Join(execDir, "ffmpeg.exe"),
			filepath.Join(execDir, "ffmpeg"),
			filepath.Join(execDir, "bin", "ffmpeg.exe"),
			filepath.Join(execDir, "bin", "ffmpeg"),
			filepath.Join(execDir, "..", "Resources", "ffmpeg"),
			filepath.Join(execDir, "..", "Resources", "bin", "ffmpeg"),
		)
	}

	// 1. 检测应用配置存储目录的 bin 子目录
	appBinDir := filepath.Join(GetConfigDir(), "bin")
	candidates = append(candidates,
		filepath.Join(appBinDir, "ffmpeg.exe"),
		filepath.Join(appBinDir, "ffmpeg"),
	)

	// 2. 操作系统常见路径
	if runtime.GOOS == "windows" {
		localApp := os.Getenv("LOCALAPPDATA")
		appData := os.Getenv("APPDATA")
		candidates = append(candidates,
			`C:\ffmpeg\bin\ffmpeg.exe`,
			`C:\Program Files\ffmpeg\bin\ffmpeg.exe`,
			`C:\Program Files (x86)\ffmpeg\bin\ffmpeg.exe`,
			filepath.Join(localApp, `Microsoft\WinGet\Packages\Gyan.FFmpeg_Microsoft.Winget.Source_8wekyb3d8bbwe\ffmpeg-7.0.2-full_build\bin\ffmpeg.exe`),
			filepath.Join(localApp, `Programs\bilibili下载器专业版\ffmpeg.exe`),
			filepath.Join(appData, `bilibili-downloader-pro\bin\ffmpeg.exe`),
		)
	} else {
		candidates = append(candidates,
			"/opt/homebrew/bin/ffmpeg",
			"/usr/local/bin/ffmpeg",
			"/usr/bin/ffmpeg",
			"/opt/local/bin/ffmpeg",
		)
	}

	for _, p := range candidates {
		if p == "" {
			continue
		}
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			if out, err := exec.Command(p, "-version").Output(); err == nil && len(out) > 0 {
				return p
			}
		}
	}

	// 3. 检查系统 PATH
	if p, err := exec.LookPath("ffmpeg.exe"); err == nil {
		if out, err := exec.Command(p, "-version").Output(); err == nil && len(out) > 0 {
			return p
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		if out, err := exec.Command(p, "-version").Output(); err == nil && len(out) > 0 {
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
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings = s

	// 如果设置中未指定或旧路径不存在，自动更新检测最新路径
	if m.settings.FFmpegPath == "" {
		m.settings.FFmpegPath = DetectFFmpeg()
	} else if _, err := os.Stat(m.settings.FFmpegPath); err != nil {
		m.settings.FFmpegPath = DetectFFmpeg()
	}

	return nil
}

func (m *ConfigManager) Save(s Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings = s
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	filePath := filepath.Join(m.dir, "settings.json")
	return os.WriteFile(filePath, data, 0644)
}

func (m *ConfigManager) Get() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings
}

func (m *ConfigManager) UpdateFFmpegPath(path string) error {
	m.mu.Lock()
	m.settings.FFmpegPath = path
	s := m.settings
	m.mu.Unlock()
	return m.Save(s)
}

func (m *ConfigManager) GetCookiesPath() string {
	return filepath.Join(m.dir, "cookies.json")
}

func (m *ConfigManager) GetTasksPath() string {
	return filepath.Join(m.dir, "tasks.json")
}
