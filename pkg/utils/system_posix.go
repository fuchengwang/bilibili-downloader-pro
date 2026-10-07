//go:build !windows

package utils

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

func openDirectoryOS(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
			cmd = exec.Command("open", "-R", target)
		} else {
			cmd = exec.Command("open", target)
		}
	default:
		if fi, err := os.Stat(target); err == nil && !fi.IsDir() {
			cmd = exec.Command("xdg-open", filepath.Dir(target))
		} else {
			cmd = exec.Command("xdg-open", target)
		}
	}
	return runOpenCommand(cmd)
}

func openFileOS(path string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", path)
	} else {
		cmd = exec.Command("xdg-open", path)
	}
	return runOpenCommand(cmd)
}

func runOpenCommand(cmd *exec.Cmd) error {
	if runtime.GOOS == "darwin" {
		// open 默认在请求交给系统后退出，不等待 Finder 或播放器关闭。
		return cmd.Run()
	}
	// xdg-open 可能持续等待播放器；保持异步打开，同时回收子进程。
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// HideWindowSysProcAttr POSIX (macOS / Linux) 平台无需隐藏窗口属性，返回 nil
func HideWindowSysProcAttr() *syscall.SysProcAttr {
	return nil
}
