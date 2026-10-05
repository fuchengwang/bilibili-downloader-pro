//go:build windows

package updater

import (
	"archive/zip"
	"context"
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"bilibili_downloader/pkg/update"
	"golang.org/x/sys/windows"
)

func installationTarget(executable string) (string, error) {
	path, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	return filepath.Abs(path)
}
func preparePackage(ctx context.Context, packagePath string, release update.Release, p *Plan, directory string) error {
	if !strings.EqualFold(filepath.Ext(packagePath), ".zip") {
		return errors.New("Windows 更新包需要为 ZIP")
	}
	p.Prepared = filepath.Join(filepath.Dir(p.Target), ".bbdown-update-"+p.Token+".exe")
	p.Backup = filepath.Join(filepath.Dir(p.Target), ".bbdown-previous-"+p.Token+".exe")
	archive, err := zip.OpenReader(packagePath)
	if err != nil {
		return errors.New("无法打开更新安装包")
	}
	defer archive.Close()
	var executable *zip.File
	for _, entry := range archive.File {
		if entry.Name == "BBDown Pro.exe" {
			if executable != nil {
				return errors.New("安装包有重复程序文件")
			}
			executable = entry
		}
	}
	if executable == nil || !executable.Mode().IsRegular() || executable.UncompressedSize64 == 0 || executable.UncompressedSize64 > 512<<20 {
		return errors.New("安装包中没有有效的 BBDown Pro.exe")
	}
	in, err := executable.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(p.Prepared, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return fmt.Errorf("无法准备更新，请确认软件所在目录可写: %w", err)
	}
	complete := false
	defer func() {
		_ = out.Close()
		if !complete {
			_ = os.Remove(p.Prepared)
		}
	}()
	// Only this exact root executable is extracted; no archive paths are used.
	n, err := io.Copy(out, io.LimitReader(&contextReader{ctx: ctx, r: in}, int64(executable.UncompressedSize64)+1))
	if err != nil {
		return err
	}
	if n != int64(executable.UncompressedSize64) {
		return errors.New("安装包大小不正确")
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	binary, err := pe.Open(p.Prepared)
	if err != nil {
		return errors.New("更新程序格式无效")
	}
	machine := uint16(pe.IMAGE_FILE_MACHINE_AMD64)
	if runtime.GOARCH == "arm64" {
		machine = pe.IMAGE_FILE_MACHINE_ARM64
	}
	match := binary.Machine == machine
	binary.Close()
	if !match {
		return errors.New("安装包不支持当前 Windows")
	}
	if err := checkExecutableVersion(ctx, p.Prepared, release.Version); err != nil {
		return err
	}
	complete = true
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func validatePlatformPlan(p *Plan) error {
	if p.BinaryRelative != "" || !strings.EqualFold(filepath.Ext(p.Target), ".exe") || p.Prepared != filepath.Join(filepath.Dir(p.Target), ".bbdown-update-"+p.Token+".exe") || p.Backup != filepath.Join(filepath.Dir(p.Target), ".bbdown-previous-"+p.Token+".exe") {
		return errors.New("invalid Windows update paths")
	}
	return nil
}
func preparedBinary(p *Plan) string { return p.Prepared }
func helperSuffix() string          { return ".exe" }
func detachCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW, HideWindow: true}
}
func quietCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
func waitParent(ctx context.Context, pid int) error {
	if pid <= 0 || pid == os.Getpid() {
		return errors.New("invalid parent process")
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	for {
		result, err := windows.WaitForSingleObject(handle, 100)
		if err != nil {
			return err
		}
		if result == windows.WAIT_OBJECT_0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

var replaceFileW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

func nativeReplace(target, replacement, backup string) error {
	a, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	b, err := windows.UTF16PtrFromString(replacement)
	if err != nil {
		return err
	}
	c, err := windows.UTF16PtrFromString(backup)
	if err != nil {
		return err
	}
	success, _, callErr := replaceFileW.Call(uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(c)), 0, 0, 0)
	if success == 0 {
		return callErr
	}
	return nil
}
func replaceInstallation(p *Plan) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := nativeReplace(p.Target, p.Prepared, p.Backup)
		if err == nil {
			return nil
		}
		// ReplaceFile may have moved the original to its backup before failing.
		// Recover that name immediately so the old program stays launchable.
		if _, statErr := os.Stat(p.Target); errors.Is(statErr, os.ErrNotExist) {
			if _, backupErr := os.Stat(p.Backup); backupErr == nil {
				if restoreErr := os.Rename(p.Backup, p.Target); restoreErr != nil {
					return fmt.Errorf("旧版本保存在 %s: %w", p.Backup, restoreErr)
				}
			}
		}
		if (!errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED)) || time.Now().After(deadline) {
			return fmt.Errorf("无法替换程序，请关闭占用该文件的程序后重试: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
func restoreInstallation(p *Plan) error {
	if _, err := os.Stat(p.Target); errors.Is(err, os.ErrNotExist) {
		return os.Rename(p.Backup, p.Target)
	}
	// Keep the candidate available for an explicit retry, atomically restore old.
	_ = os.Remove(p.Prepared)
	return nativeReplace(p.Target, p.Backup, p.Prepared)
}
func launchApplication(target string) error {
	cmd := exec.Command(target)
	cmd.Dir = filepath.Dir(target)
	detachCommand(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
func lockInstaller(directory string) (func(), error) {
	path, err := windows.UTF16PtrFromString(filepath.Join(directory, "install.lock"))
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return nil, errInstallBusy
	}
	if err != nil {
		return nil, err
	}
	return func() { _ = windows.CloseHandle(handle) }, nil
}
