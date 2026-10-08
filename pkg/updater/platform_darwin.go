//go:build darwin

package updater

import (
	"context"
	"debug/macho"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"bilibili_downloader/pkg/update"
	"golang.org/x/sys/unix"
)

func installationTarget(executable string) (string, error) {
	path, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	target := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	if !strings.HasSuffix(target, ".app") || filepath.Dir(path) != filepath.Join(target, "Contents", "MacOS") {
		return "", errors.New("请将 BBDown Pro 安装到本机后再更新")
	}
	return target, nil
}

func preparePackage(ctx context.Context, packagePath string, release update.Release, p *Plan, directory string) error {
	if !strings.EqualFold(filepath.Ext(packagePath), ".dmg") {
		return errors.New("macOS 更新包需要为 DMG")
	}
	p.Prepared = filepath.Join(filepath.Dir(p.Target), ".bbdown-update-"+p.Token+".app")
	p.Backup = p.Prepared // RENAME_SWAP leaves the old complete bundle here.
	name, err := plistValue(ctx, p.Target, "CFBundleExecutable")
	if err != nil || name == "" || filepath.Base(name) != name {
		return errors.New("无法读取当前软件信息")
	}
	p.BinaryRelative = filepath.Join("Contents", "MacOS", name)
	if _, err := os.Lstat(p.Prepared); !errors.Is(err, os.ErrNotExist) {
		return errors.New("更新临时目录已存在，请重试")
	}
	mount, err := os.MkdirTemp(directory, "mount-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mount)
	// Even a cancelled attach can have mounted the disk before the command
	// returned. Always detach before removing the mountpoint.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "/usr/bin/hdiutil", "detach", mount, "-force").Run()
	}()
	cmd := exec.CommandContext(ctx, "/usr/bin/hdiutil", "attach", packagePath, "-readonly", "-nobrowse", "-mountpoint", mount)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("无法打开更新安装包: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	source := filepath.Join(mount, "BBDown Pro.app")
	if info, err := os.Lstat(source); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("安装包中没有 BBDown Pro.app")
	}
	if out, err := exec.CommandContext(ctx, "/usr/bin/ditto", source, p.Prepared).CombinedOutput(); err != nil {
		return fmt.Errorf("无法准备更新，请确认软件所在目录可写: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if err := validateMacBundle(ctx, p.Target, p.Prepared, release.Version); err != nil {
		return err
	}
	if err := checkMachArchitecture(preparedBinary(p)); err != nil {
		return err
	}
	return checkExecutableVersion(ctx, preparedBinary(p), release.Version)
}

func validateMacBundle(ctx context.Context, current, candidate, version string) error {
	if err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", candidate).Run(); err != nil {
		return errors.New("更新包签名无效，请重新下载")
	}
	oldID, e1 := plistValue(ctx, current, "CFBundleIdentifier")
	newID, e2 := plistValue(ctx, candidate, "CFBundleIdentifier")
	if e1 != nil || e2 != nil || oldID == "" || oldID != newID {
		return errors.New("更新包不属于 BBDown Pro")
	}
	newVersion, err := plistValue(ctx, candidate, "CFBundleShortVersionString")
	if err != nil || newVersion != version {
		return errors.New("安装包版本与后台发布版本不一致")
	}
	oldTeam := signingTeam(ctx, current)
	newTeam := signingTeam(ctx, candidate)
	if oldTeam != "" && oldTeam != newTeam {
		return errors.New("更新包的开发者签名与当前版本不一致")
	}
	return nil
}

func plistValue(ctx context.Context, bundle, key string) (string, error) {
	out, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-extract", key, "raw", "-o", "-", filepath.Join(bundle, "Contents", "Info.plist")).Output()
	return strings.TrimSpace(string(out)), err
}
func signingTeam(ctx context.Context, bundle string) string {
	out, _ := exec.CommandContext(ctx, "/usr/bin/codesign", "-dv", "--verbose=4", bundle).CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "TeamIdentifier=") {
			team := strings.TrimPrefix(line, "TeamIdentifier=")
			if team != "not set" {
				return team
			}
		}
	}
	return ""
}
func checkMachArchitecture(path string) error {
	expected := macho.CpuArm64
	if runtime.GOARCH == "amd64" {
		expected = macho.CpuAmd64
	}
	if fat, err := macho.OpenFat(path); err == nil {
		defer fat.Close()
		for _, arch := range fat.Arches {
			if arch.Cpu == expected {
				return nil
			}
		}
		return errors.New("安装包不支持当前 Mac")
	}
	binary, err := macho.Open(path)
	if err != nil {
		return errors.New("更新程序格式无效")
	}
	defer binary.Close()
	if binary.Cpu != expected {
		return errors.New("安装包不支持当前 Mac")
	}
	return nil
}
func validatePlatformPlan(p *Plan) error {
	if !strings.HasSuffix(p.Target, ".app") || p.Prepared != filepath.Join(filepath.Dir(p.Target), ".bbdown-update-"+p.Token+".app") || p.Backup != p.Prepared {
		return errors.New("invalid macOS update paths")
	}
	if filepath.Dir(p.BinaryRelative) != filepath.Join("Contents", "MacOS") || filepath.Base(p.BinaryRelative) == "." || filepath.Base(p.BinaryRelative) == ".." {
		return errors.New("invalid macOS executable path")
	}
	return nil
}
func preparedBinary(p *Plan) string { return filepath.Join(p.Prepared, p.BinaryRelative) }
func helperSuffix() string          { return "" }

func prepareHelper(executable, directory, name string) (string, string, error) {
	bundle, err := installationTarget(executable)
	if err != nil {
		return "", "", err
	}
	helper := filepath.Join(directory, name+".app")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Developer ID executables are sealed with the bundle's Info.plist and
	// resources. Copying just Contents/MacOS produces a helper macOS kills.
	if out, err := exec.CommandContext(ctx, "/usr/bin/ditto", bundle, helper).CombinedOutput(); err != nil {
		_ = os.RemoveAll(helper)
		return "", "", fmt.Errorf("无法准备更新程序: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", helper).Run(); err != nil {
		_ = os.RemoveAll(helper)
		return "", "", errors.New("更新程序签名校验失败，请重新安装软件后重试")
	}
	return filepath.Join(helper, "Contents", "MacOS", filepath.Base(executable)), helper, nil
}

func detachCommand(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func quietCommand(cmd *exec.Cmd)  {}
func waitParent(ctx context.Context, pid int) error {
	if pid <= 0 || pid == os.Getpid() {
		return errors.New("invalid parent process")
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func replaceInstallation(p *Plan) error {
	return unix.RenamexNp(p.Target, p.Prepared, unix.RENAME_SWAP)
}
func restoreInstallation(p *Plan) error     { return unix.RenamexNp(p.Target, p.Backup, unix.RENAME_SWAP) }
func launchApplication(target string) error { return exec.Command("/usr/bin/open", "-n", target).Run() }
func startApplicationForUpdate(target string) (*exec.Cmd, error) {
	// -W observes a new app that exits before its window is ready. The watcher
	// can be stopped after acknowledgement without terminating the application.
	cmd := exec.Command("/usr/bin/open", "-n", "-W", target)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd, cmd.Start()
}
func stopLaunchMonitor(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
func lockInstaller(directory string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(directory, "install.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errInstallBusy
		}
		return nil, err
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
}
