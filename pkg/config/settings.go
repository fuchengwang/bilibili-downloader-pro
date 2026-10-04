package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"bilibili_downloader/pkg/utils"
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
	cacheDir string
	settings Settings
}

var (
	instance *ConfigManager
	once     sync.Once
)

const (
	defaultMaxConcurrent  = 3
	defaultThreadsPerTask = 4
	maxMaxConcurrent      = 10
	maxThreadsPerTask     = 8
)

func defaultSettings(downloadDir string) Settings {
	return Settings{
		DownloadDir:      downloadDir,
		DefaultQuality:   "highest",
		DefaultCodec:     "auto",
		MaxConcurrent:    defaultMaxConcurrent,
		ThreadsPerTask:   defaultThreadsPerTask,
		AutoMerge:        true,
		DeleteTempFiles:  true,
		AutoClipboard:    true,
		FileNameTemplate: "{title} - {part}",
		Theme:            "dark",
	}
}

func normalizeSettings(s Settings, fallbackDownloadDir string) Settings {
	if strings.TrimSpace(s.DownloadDir) == "" {
		s.DownloadDir = fallbackDownloadDir
	}
	if strings.TrimSpace(s.DefaultQuality) == "" {
		s.DefaultQuality = "highest"
	}
	if strings.TrimSpace(s.DefaultCodec) == "" {
		s.DefaultCodec = "auto"
	}
	if s.MaxConcurrent <= 0 {
		s.MaxConcurrent = defaultMaxConcurrent
	} else if s.MaxConcurrent > maxMaxConcurrent {
		s.MaxConcurrent = maxMaxConcurrent
	}
	if s.ThreadsPerTask <= 0 {
		s.ThreadsPerTask = defaultThreadsPerTask
	} else if s.ThreadsPerTask > maxThreadsPerTask {
		s.ThreadsPerTask = maxThreadsPerTask
	}
	if strings.TrimSpace(s.FileNameTemplate) == "" {
		s.FileNameTemplate = "{title} - {part}"
	}
	s.Theme = strings.ToLower(strings.TrimSpace(s.Theme))
	if s.Theme != "dark" && s.Theme != "light" && s.Theme != "system" {
		s.Theme = "dark"
	}
	return s
}

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

// DefaultDownloadDir 获取默认下载目录 (macOS / 系统原生 Downloads 目录)
func DefaultDownloadDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, "Downloads")
		_ = os.MkdirAll(dir, 0755)
		return dir
	}
	return "./downloads"
}

// NewConfigManager 创建一个独立的配置管理器实例（用于测试与环境隔离）
func NewConfigManager(dir string) *ConfigManager {
	_ = os.MkdirAll(dir, 0755)
	mgr := &ConfigManager{
		dir:      dir,
		cacheDir: filepath.Join(dir, "cache", "downloads"),
		settings: defaultSettings(filepath.Join(dir, "downloads")),
	}
	_ = mgr.load()
	return mgr
}

// GetManager 获取单例配置管理器
func GetManager() *ConfigManager {
	once.Do(func() {
		cfgDir := GetConfigDir()
		cacheRoot, err := os.UserCacheDir()
		if err != nil {
			cacheRoot = os.TempDir()
		}
		mgr := &ConfigManager{
			dir:      cfgDir,
			cacheDir: filepath.Join(cacheRoot, "bilibili-downloader-pro", "downloads"),
			settings: defaultSettings(DefaultDownloadDir()),
		}
		_ = mgr.load()
		instance = mgr
	})
	return instance
}

// GetDownloadCacheDir returns the platform cache location; isolated managers keep
// their temporary downloads inside their own sandbox.
func (m *ConfigManager) GetDownloadCacheDir() string {
	return m.cacheDir
}

func (m *ConfigManager) load() error {
	filePath := filepath.Join(m.dir, "settings.json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	isGlobalConfigDir := filepath.Clean(m.dir) == filepath.Clean(GetConfigDir())
	fallbackDownloadDir := filepath.Join(m.dir, "downloads")
	if isGlobalConfigDir {
		fallbackDownloadDir = DefaultDownloadDir()
	}
	// Unmarshal over defaults so fields introduced in a newer version do not
	// silently become zero values when an older settings file is reopened.
	s := defaultSettings(fallbackDownloadDir)
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	s = normalizeSettings(s, fallbackDownloadDir)

	// 核心安全自愈防护：仅针对正式用户配置目录，如果持久化配置中的下载目录为空、
	// 包含历史测试临时特征、或指向已不存在的临时目录，自动自愈纠偏重置为系统原生 Downloads 目录
	if isGlobalConfigDir {
		tempDir := os.TempDir()
		isInvalidTemp := s.DownloadDir == "" || strings.Contains(s.DownloadDir, "bili_live_test")
		if !isInvalidTemp && (strings.HasPrefix(s.DownloadDir, "/var/folders/") || (tempDir != "" && strings.HasPrefix(s.DownloadDir, tempDir))) {
			if fi, err := os.Stat(s.DownloadDir); err != nil || !fi.IsDir() {
				isInvalidTemp = true
			}
		}

		if isInvalidTemp {
			s.DownloadDir = DefaultDownloadDir()
			data, _ = json.MarshalIndent(s, "", "  ")
			_ = utils.AtomicWriteFile(filePath, data, 0644)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.settings = s
	return nil
}

func (m *ConfigManager) Save(s Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s = normalizeSettings(s, m.defaultDownloadDir())
	m.settings = s
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	filePath := filepath.Join(m.dir, "settings.json")
	return utils.AtomicWriteFile(filePath, data, 0644)
}

func (m *ConfigManager) defaultDownloadDir() string {
	if filepath.Clean(m.dir) == filepath.Clean(GetConfigDir()) {
		return DefaultDownloadDir()
	}
	return filepath.Join(m.dir, "downloads")
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

// GetBinDir 获取用户数据目录下的内置二进制程序目录 (用于持久化自愈部署的 ffmpeg 等)
func (m *ConfigManager) GetBinDir() string {
	binDir := filepath.Join(m.dir, "bin")
	_ = os.MkdirAll(binDir, 0755)
	return binDir
}
