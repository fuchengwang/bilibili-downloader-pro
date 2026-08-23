//go:build windows

package utils

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func runCmdHidden(name string, arg ...string) error {
	cmd := exec.Command(name, arg...)
	// 在 Windows 上隐藏可能弹出的命令行窗口
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

// openDirectoryOS 在 Windows 上打开目录或高亮选中指定文件
func openDirectoryOS(target string) error {
	target = filepath.Clean(target)
	fi, err := os.Stat(target)
	if err == nil && !fi.IsDir() {
		// 目标是文件：调用 explorer.exe /select,target
		return runCmdHidden("explorer", fmt.Sprintf("/select,%s", target))
	}

	// 目标是目录：直接通过系统 Shell 打开该文件夹
	dir := target
	if err != nil {
		dir = filepath.Dir(target)
	}
	_ = os.MkdirAll(dir, 0755)
	return runCmdHidden("explorer", dir)
}

// openFileOS 在 Windows 上使用系统默认关联播放器打开指定媒体文件
func openFileOS(path string) error {
	path = filepath.Clean(path)
	// 使用 start 命令，第一个参数为空字符串以防止标题解析问题
	return runCmdHidden("cmd", "/c", "start", "", path)
}

