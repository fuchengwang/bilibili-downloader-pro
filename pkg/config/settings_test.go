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
