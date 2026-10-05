//go:build windows

package updater

import (
	"archive/zip"
	"bilibili_downloader/pkg/update"
	"context"
	"os"
	"path/filepath"
	"testing"
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
