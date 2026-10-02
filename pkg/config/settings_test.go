package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsAtomicSaveAndBinDir(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := NewConfigManager(tmpDir)

	s := mgr.Get()
	s.DownloadDir = filepath.Join(tmpDir, "custom_down")
	s.MaxConcurrent = 5

	if err := mgr.Save(s); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 验证 settings.json 存在且无残留临时文件
	jsonPath := filepath.Join(tmpDir, "settings.json")
	if _, err := os.Stat(jsonPath); err != nil {
		t.Fatalf("settings.json not found: %v", err)
	}
	tmpPath := jsonPath + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("Temporary file settings.json.tmp was not cleaned up")
	}

	// 重新读取验证
	mgr2 := NewConfigManager(tmpDir)
	s2 := mgr2.Get()
	if s2.DownloadDir != s.DownloadDir || s2.MaxConcurrent != 5 {
		t.Fatalf("Settings content mismatch: %+v", s2)
	}

	// 验证 GetBinDir 目录存在
	binDir := mgr.GetBinDir()
	if fi, err := os.Stat(binDir); err != nil || !fi.IsDir() {
		t.Fatalf("GetBinDir failed to create valid directory: %v", err)
	}
}

func TestSettingsLoadMergesDefaultsForLegacyConfig(t *testing.T) {
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "settings.json")
	legacy := `{"downloadDir":"","autoMerge":false}`
	if err := os.WriteFile(settingsPath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}

	mgr := NewConfigManager(tmpDir)
	s := mgr.Get()
	if s.DownloadDir != filepath.Join(tmpDir, "downloads") {
		t.Fatalf("空下载目录应回退到独立配置目录下的 downloads，got %q", s.DownloadDir)
	}
	if s.DefaultQuality != "highest" || s.DefaultCodec != "auto" || s.MaxConcurrent != 3 || s.ThreadsPerTask != 4 {
		t.Fatalf("旧配置缺失字段未合并默认值: %+v", s)
	}
	if s.AutoMerge {
		t.Fatal("显式保存的 false 不能被默认值覆盖")
	}
	if !s.DeleteTempFiles || !s.AutoClipboard || s.FileNameTemplate == "" || s.Theme == "" {
		t.Fatalf("旧配置的布尔/模板/主题默认值未恢复: %+v", s)
	}
}

func TestSettingsSaveNormalizesEmptyValues(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := NewConfigManager(tmpDir)
	if err := mgr.Save(Settings{MaxConcurrent: 100, ThreadsPerTask: 100}); err != nil {
		t.Fatal(err)
	}
	s := mgr.Get()
	if s.DownloadDir != filepath.Join(tmpDir, "downloads") || s.MaxConcurrent != maxMaxConcurrent || s.ThreadsPerTask != maxThreadsPerTask {
		t.Fatalf("Save 异常并发设置未正确限制: %+v", s)
	}
	if s.DefaultQuality != "highest" || s.DefaultCodec != "auto" || s.FileNameTemplate == "" || s.Theme == "" {
		t.Fatalf("Save 空设置未恢复字符串默认值: %+v", s)
	}
}
