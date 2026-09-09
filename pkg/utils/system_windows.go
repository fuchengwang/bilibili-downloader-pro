//go:build windows

package utils

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// openDirectoryOS 在 Windows 上打开目录或高亮选中指定文件
func openDirectoryOS(target string) error {
	target = filepath.Clean(target)
	fi, err := os.Stat(target)
	if err == nil && !fi.IsDir() {
		// 目标是文件：调用 explorer.exe /select,"<path>" 紧凑格式，防止路径识别失败退化为打开根目录
		cmd := exec.Command("explorer", fmt.Sprintf("/select,%s", target))
		return cmd.Start()
	}

	// 目标是目录：直接通过系统原生 ShellExecute 打开，无命令行弹窗
	dir := target
	if err != nil {
		dir = filepath.Dir(target)
	}
	_ = os.MkdirAll(dir, 0755)

	verbPtr, _ := windows.UTF16PtrFromString("open")
	dirPtr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verbPtr, dirPtr, nil, nil, windows.SW_SHOWNORMAL)
}

// openFileOS 在 Windows 上使用系统默认关联播放器打开指定媒体文件 (原生 ShellExecute，零控制台黑框)
func openFileOS(path string) error {
	path = filepath.Clean(path)
	verbPtr, _ := windows.UTF16PtrFromString("open")
	filePtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verbPtr, filePtr, nil, nil, windows.SW_SHOWNORMAL)
}

