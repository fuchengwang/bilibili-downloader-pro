//go:build windows

package updater

import (
	"archive/zip"
	"bilibili_downloader/pkg/update"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func makeNativePackage(t *testing.T, root, oldBinary, newBinary string) (string, string) {
	t.Helper()
	install := filepath.Join(root, "安装 文件")
	if err := os.MkdirAll(install, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(install, "BBDown Pro.exe")
	if err := copyFile(oldBinary, target, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(newBinary)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "BBDown-Pro-Windows.zip")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(out)
	executable, err := archive.Create("BBDown Pro.exe")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executable.Write(raw); err != nil {
		t.Fatal(err)
	}
	// This extraneous entry must never be extracted outside the staging file.
	unwanted, _ := archive.Create("../../polluted.txt")
	_, _ = unwanted.Write([]byte("not allowed"))
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return target, path
}
func TestBadWindowsPackageDoesNotDamageCurrentExe(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "BBDown Pro.exe")
	if err := os.WriteFile(target, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "bad.zip")
	out, _ := os.Create(path)
	archive := zip.NewWriter(out)
	entry, _ := archive.Create("../BBDown Pro.exe")
	_, _ = entry.Write([]byte("bad"))
	_ = archive.Close()
	_ = out.Close()
	if _, err := Prepare(context.Background(), path, update.Release{Version: "1.0.1"}, Config{Directory: root, Executable: target}); err == nil {
		t.Fatal("invalid archive accepted")
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "old" {
		t.Fatal("preparation changed current exe", err)
	}
}

func TestWindowsReplacementWaitsForFileHandleRelease(t *testing.T) {
	p := nativeFixturePlan(t, t.TempDir())
	name, err := windows.UTF16PtrFromString(p.Target)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if handle != windows.InvalidHandle {
			_ = windows.CloseHandle(handle)
		}
	}()
	done := make(chan error, 1)
	go func() { done <- replaceInstallation(p) }()
	select {
	case err := <-done:
		t.Fatal("replacement did not wait for the open file", err)
	case <-time.After(150 * time.Millisecond):
	}
	if raw, err := os.ReadFile(p.Target); err != nil || string(raw) != "old executable" {
		t.Fatal("blocked replacement damaged the old application", err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	handle = windows.InvalidHandle
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replacement did not recover after the handle closed")
	}
	if hash, _ := hashFile(p.Target); hash != p.BinaryHash {
		t.Fatal("replacement did not install the complete new executable")
	}
}
