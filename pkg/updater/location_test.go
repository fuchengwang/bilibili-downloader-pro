//go:build darwin || windows

package updater

import (
	"bilibili_downloader/pkg/update"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocationCheckAcceptsWritableCustomFolders(t *testing.T) {
	for _, folder := range []string{"Desktop", "Downloads", "自选目录 with spaces"} {
		t.Run(folder, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), folder)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			p := nativeFixturePlan(t, dir)
			if err := checkExecutableLocation(targetBinary(p)); err != nil {
				t.Fatal("writable custom installation was rejected", err)
			}
			if raw, err := os.ReadFile(targetBinary(p)); err != nil || string(raw) != "old executable" {
				t.Fatal("location check modified the application", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".bbdown-write-check-") {
					t.Fatal("probe was not cleaned up")
				}
			}
		})
	}
}

func TestDirectoryBecomingUnwritableBeforeRestartKeepsOldApplication(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Geteuid() == 0 {
		t.Skip("POSIX permissions required")
	}
	directory := t.TempDir()
	install := t.TempDir()
	p := nativeFixturePlan(t, install)
	p.AutoApply = true
	if err := WritePlan(directory, p); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(install, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(install, 0700) })
	if err := LaunchInstall(directory, targetBinary(p)); err == nil || !strings.Contains(friendlyError(err), "可写") {
		t.Fatal("unwritable location did not produce actionable error", err)
	}
	failed, err := ReadPlan(directory)
	if err != nil || failed.Phase != "failed" || failed.AutoApply || failed.HelperPID != 0 {
		t.Fatal("application was allowed to quit or automatically retry", failed, err)
	}
	if raw, err := os.ReadFile(targetBinary(p)); err != nil || string(raw) != "old executable" {
		t.Fatal("failed preflight changed the installed app", err)
	}
	log, err := os.ReadFile(filepath.Join(directory, "install.log"))
	if err != nil || !strings.Contains(string(log), "stage=schedule_failed") {
		t.Fatal("failure was not recorded", err)
	}
}

func TestLocationFailureStopsDownloadAndCanBeRetried(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Geteuid() == 0 {
		t.Skip("POSIX permissions required")
	}
	install := t.TempDir()
	p := nativeFixturePlan(t, install)
	c := &fakeClient{}
	offer(c)
	downloaded := false
	c.download = func(context.Context, update.Release, update.DownloadOptions) (string, error) {
		downloaded = true
		return "", context.Canceled
	}
	m, err := New(Config{Directory: t.TempDir(), Executable: targetBinary(p), Version: "1.0.0", Client: c})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	if err := os.Chmod(install, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(install, 0700) })
	if err := m.Download(context.Background()); err == nil || !strings.Contains(m.Snapshot().Error, "可写") {
		t.Fatal("missing location guidance", err)
	}
	if downloaded {
		t.Fatal("download began before location was checked")
	}
	if err := os.Chmod(install, 0700); err != nil {
		t.Fatal(err)
	}
	if err := m.Download(context.Background()); err != nil {
		t.Fatal("location recovery required reinstalling or resetting settings", err)
	}
	m.wg.Wait()
	if !downloaded {
		t.Fatal("fixed location could not retry download")
	}
}
