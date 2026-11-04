//go:build windows

package downloader

import (
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCollectionCleanupWindowsJunctionIsPreserved(t *testing.T) {
	m, tasks, top := cleanupCollectionFixture(t, true)
	if err := m.ClearCompleted(true); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", top, outside).CombinedOutput(); err != nil {
		t.Skipf("directory junctions unavailable: %v %s", err, out)
	}
	m.cleanupEmptyCollectionDirs(tasks)
	if _, err := os.Lstat(top); err != nil {
		t.Fatal("junction was removed")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("junction target was removed")
	}
}

func TestCollectionCleanupWindowsLockedVideoKeepsFolder(t *testing.T) {
	m, tasks, top := cleanupCollectionFixture(t, false)
	path, err := windows.UTF16PtrFromString(tasks[0].OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := m.ClearCompleted(true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tasks[0].OutputPath); err != nil {
		t.Fatal("video with delete-denying handle disappeared")
	}
	if _, err := os.Stat(top); err != nil {
		t.Fatal("folder containing a locked video was removed")
	}
}
