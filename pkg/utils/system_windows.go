//go:build windows

package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// openDirectoryOS 在 Windows 上打开目录或高亮选中指定文件
func openDirectoryOS(target string) error {
	target = filepath.Clean(target)
	fi, err := os.Stat(target)
	if err == nil && !fi.IsDir() {
		// 目标是文件：通过原生 ShellExecute 调起 explorer 并传递 /select,"<path>"
		// 彻底规避 exec.Command 字符串转义导致包含空格与逗号时无法定位高亮的问题
		verbPtr, _ := windows.UTF16PtrFromString("open")
		explorerPtr, _ := windows.UTF16PtrFromString("explorer.exe")
		paramPtr, _ := windows.UTF16PtrFromString(fmt.Sprintf(`/select,"%s"`, target))
		return windows.ShellExecute(0, verbPtr, explorerPtr, paramPtr, nil, windows.SW_SHOWNORMAL)
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

// HideWindowSysProcAttr Windows 平台专用：彻底消除调用 FFmpeg 等外部工具时的控制台黑框
func HideWindowSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}



