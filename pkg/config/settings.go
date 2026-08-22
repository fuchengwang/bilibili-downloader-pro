package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Settings 存储用户全局偏好设置
type Settings struct {
	DownloadDir      string `json:"downloadDir"`      // 下载存储目录
	DefaultQuality   string `json:"defaultQuality"`   // 默认清晰度: "highest", "127", "120", "116", "80", "64", "32"
	DefaultCodec     string `json:"defaultCodec"`     // 默认编码: "auto", "AVC", "HEVC", "AV1"
	MaxConcurrent    int    `json:"maxConcurrent"`    // 最大同时下载任务数
	ThreadsPerTask   int    `json:"threadsPerTask"`   // 单任务并发分块数
	AutoMerge        bool   `json:"autoMerge"`        // 是否自动合成音视频 (原生 Go 引擎)
	DeleteTempFiles  bool   `json:"deleteTempFiles"`  // 合成后是否自动删除临时分块文件
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

func (m *ConfigManager) GetCookiesPath() string {
	return filepath.Join(m.dir, "cookies.json")
}

func (m *ConfigManager) GetTasksPath() string {
	return filepath.Join(m.dir, "tasks.json")
}
