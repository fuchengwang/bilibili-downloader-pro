package license

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestStorageSaveReplacesExistingToken(t *testing.T) {
	storagePath := filepath.Join(t.TempDir(), "license.dat")
	storage, err := NewStorage("bbdown-pro", "device-1", storagePath)
	if err != nil {
		t.Fatal(err)
	}

	first := &LicenseToken{AppID: "bbdown-pro", LicenseKey: "first-key", DeviceID: "device-1", IsPermanent: true}
	if err := storage.Save(first); err != nil {
		t.Fatalf("保存初始授权失败: %v", err)
	}
	second := &LicenseToken{AppID: "bbdown-pro", LicenseKey: "second-key", DeviceID: "device-1", IsPermanent: true}
	if err := storage.Save(second); err != nil {
		t.Fatalf("覆盖已有授权失败: %v", err)
	}

	loaded, err := storage.Load("device-1")
	if err != nil {
		t.Fatalf("读取覆盖后的授权失败: %v", err)
	}
	if loaded.LicenseKey != second.LicenseKey {
		t.Fatalf("读取到旧授权: got %q, want %q", loaded.LicenseKey, second.LicenseKey)
	}
}

func TestStorageSaveRejectsNilToken(t *testing.T) {
	storage, err := NewStorage("bbdown-pro", "device-1", filepath.Join(t.TempDir(), "license.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Save(nil); err == nil || errors.Is(err, ErrNoLicenseFound) {
		t.Fatalf("nil 授权应返回参数错误，实际: %v", err)
	}
}
