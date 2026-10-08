package updater

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"bilibili_downloader/pkg/update"
	"bilibili_downloader/pkg/utils"
)

type Plan struct {
	Token          string `json:"token"`
	Target         string `json:"target"`
	Prepared       string `json:"prepared"`
	Backup         string `json:"backup"`
	BinaryRelative string `json:"binaryRelative"`
	BinaryHash     string `json:"binaryHash"`
	Version        string `json:"version"`
	Phase          string `json:"phase"`
	AutoApply      bool   `json:"autoApply"`
	ParentPID      int    `json:"parentPid"`
	HelperPID      int    `json:"helperPid,omitempty"`
	Error          string `json:"error"`
}

type openedInstallation struct {
	Token      string `json:"token"`
	Version    string `json:"version"`
	BinaryHash string `json:"binaryHash"`
}

func ReadPlan(directory string) (*Plan, error) {
	raw, err := os.ReadFile(filepath.Join(directory, "install.json"))
	if err != nil {
		return nil, err
	}
	var plan Plan
	if err = json.Unmarshal(raw, &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}
func WritePlan(directory string, p *Plan) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return utils.AtomicWriteFile(filepath.Join(directory, "install.json"), raw, 0600)
}

func validatePlan(p *Plan, directory string) error {
	if _, err := hex.DecodeString(p.Token); err != nil || len(p.Token) != 32 {
		return errors.New("invalid update token")
	}
	if _, err := hex.DecodeString(p.BinaryHash); err != nil || len(p.BinaryHash) != 64 {
		return errors.New("invalid prepared package hash")
	}
	if _, _, err := update.ParseVersion(p.Version); err != nil {
		return err
	}
	if !filepath.IsAbs(p.Target) || !filepath.IsAbs(p.Prepared) || !filepath.IsAbs(p.Backup) {
		return errors.New("update paths must be absolute")
	}
	if filepath.Dir(p.Target) != filepath.Dir(p.Prepared) || filepath.Dir(p.Target) != filepath.Dir(p.Backup) {
		return errors.New("update replacement must be on the same filesystem")
	}
	return validatePlatformPlan(p)
}

func Prepare(ctx context.Context, packagePath string, release update.Release, cfg Config) (*Plan, error) {
	target, err := installationTarget(cfg.Executable)
	if err != nil {
		return nil, err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	p := &Plan{Token: hex.EncodeToString(token[:]), Target: target, Version: release.Version, Phase: "ready", AutoApply: true}
	complete := false
	defer func() {
		if !complete && p.Prepared != "" {
			_ = os.RemoveAll(p.Prepared)
		}
	}()
	if err := preparePackage(ctx, packagePath, release, p, cfg.Directory); err != nil {
		return nil, err
	}
	p.BinaryHash, err = hashFile(preparedBinary(p))
	if err != nil {
		return nil, err
	}
	if err := validatePlan(p, cfg.Directory); err != nil {
		return nil, err
	}
	complete = true
	return p, nil
}

// The application is already single-instance when this is called. If launching
// the helper fails, the old application continues normally and remains retryable.
func LaunchPending(directory, executable string, currentVersion string) (bool, error) {
	p, err := ReadPlan(directory)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if p.Version == currentVersion || !p.AutoApply || p.Phase != "ready" {
		return false, nil
	}
	if err := LaunchInstall(directory, executable); err != nil {
		return false, err
	}
	return true, nil
}

// Called before acquiring the application's single-instance lock. A second
// launch of the old executable must not keep it open while the helper replaces
// it, or hold the instance lock that the new app needs. The new version itself
// is allowed through even while the helper is finishing its launch command.
func ContinueInstallation(directory, currentVersion string) (bool, error) {
	p, err := ReadPlan(directory)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, nil
	}
	if p.Version == currentVersion || validatePlan(p, directory) != nil || (p.Phase != "scheduled" && p.Phase != "applied") {
		return false, nil
	}
	deadline := time.Now().Add(300 * time.Millisecond)
	for {
		unlock, err := lockInstaller(directory)
		if errors.Is(err, errInstallBusy) {
			return true, nil
		}
		if err != nil {
			return false, nil
		}
		unlock()
		if hash, err := hashFile(targetBinary(p)); err == nil && hash == p.BinaryHash {
			return true, launchApplication(p.Target)
		}
		if p.Phase != "scheduled" || time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func LaunchInstall(directory, executable string) (resultErr error) {
	unlock, err := lockInstaller(directory)
	if err != nil {
		return err
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	p, err := ReadPlan(directory)
	if err != nil {
		return err
	}
	// Any scheduling failure should reopen the normal app on future launches,
	// rather than automatically repeating a failed installation every time.
	defer func() {
		if resultErr != nil {
			p.AutoApply = false
			p.Phase = "failed"
			p.Error = "更新未能安装，可以重试：" + resultErr.Error()
			_ = WritePlan(directory, p)
		}
	}()
	if err := validatePlan(p, directory); err != nil {
		return err
	}
	target, err := installationTarget(executable)
	if err != nil {
		return err
	}
	if target != p.Target {
		return errors.New("软件位置已改变，请重新下载更新")
	}
	if p.Phase != "ready" && p.Phase != "failed" && p.Phase != "scheduled" {
		return errors.New("更新已在处理中")
	}
	if hash, err := hashFile(preparedBinary(p)); err != nil || hash != p.BinaryHash {
		return errors.New("更新文件已损坏，请重新下载")
	}
	helperDir := filepath.Join(directory, "helper")
	if err := os.MkdirAll(helperDir, 0700); err != nil {
		return err
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	helper, helperRoot, err := prepareHelper(executable, helperDir, "apply-"+hex.EncodeToString(nonce[:]))
	if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			_ = os.RemoveAll(helperRoot)
		}
	}()
	logFile, err := os.OpenFile(filepath.Join(directory, "install.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	p.ParentPID = os.Getpid()
	p.HelperPID = 0
	p.AutoApply = false
	p.Phase = "scheduled"
	p.Error = ""
	if err := WritePlan(directory, p); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(directory, "opened.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cmd := exec.Command(helper, "--bbdown-apply-update", filepath.Join(directory, "install.json"), p.Token)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detachCommand(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Process creation alone does not mean the helper can run (macOS can kill
	// an invalid signed executable before main). Let it take the installer lock
	// and validate the plan before the caller is allowed to quit.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); close(done) }()
	unlock()
	unlock = nil
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := waitHelperReady(ctx, directory, p.Token, cmd.Process.Pid, done); err != nil {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		return fmt.Errorf("更新程序未能就绪，软件未退出，请重试: %w", err)
	}
	return nil
}

func waitHelperReady(ctx context.Context, directory, token string, pid int, done <-chan error) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if err == nil {
				err = errors.New("更新程序提前退出")
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if p, err := ReadPlan(directory); err == nil && p.Token == token && p.HelperPID == pid && p.Phase == "scheduled" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func HandleHelperArgs(args []string) (bool, error) {
	if len(args) == 0 || args[0] != "--bbdown-apply-update" {
		return false, nil
	}
	if len(args) != 2 && len(args) != 3 {
		return true, errors.New("missing update plan")
	}
	directory := filepath.Dir(args[1])
	unlock, err := waitInstallerLock(directory)
	if err != nil {
		return true, err
	}
	defer unlock()
	p, err := ReadPlan(directory)
	if err != nil {
		return true, err
	}
	if err := validatePlan(p, directory); err != nil {
		return true, err
	}
	if len(args) == 3 && args[2] != p.Token {
		return true, errors.New("update plan changed before helper startup")
	}
	if p.Phase != "scheduled" {
		return true, errors.New("update was not scheduled")
	}
	if hash, err := hashFile(preparedBinary(p)); err != nil || hash != p.BinaryHash {
		return true, errors.New("更新文件校验失败，请重新下载")
	}
	p.HelperPID = os.Getpid()
	if err := WritePlan(directory, p); err != nil {
		return true, err
	}
	err = applyPlan(directory, p)
	return true, err
}

func applyPlan(directory string, p *Plan) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := waitParent(ctx, p.ParentPID); err != nil {
		return failPlan(directory, p, err)
	}
	if hash, err := hashFile(targetBinary(p)); err == nil && hash == p.BinaryHash {
		p.Phase = "applied"
		p.AutoApply = false
		if err := WritePlan(directory, p); err != nil {
			return failPlan(directory, p, err)
		}
		return launchAndConfirm(directory, p)
	}
	hash, err := hashFile(preparedBinary(p))
	if err != nil || hash != p.BinaryHash {
		return failPlan(directory, p, errors.New("更新文件校验失败，请重新下载"))
	}
	if err := replaceInstallation(p); err != nil {
		return failPlan(directory, p, err)
	}
	if hash, err := hashFile(targetBinary(p)); err != nil || hash != p.BinaryHash {
		_ = restoreInstallation(p)
		return failPlan(directory, p, errors.New("安装后的程序校验失败"))
	}
	p.Phase = "applied"
	p.AutoApply = false
	p.Error = ""
	if err := WritePlan(directory, p); err != nil {
		_ = restoreInstallation(p)
		return failPlan(directory, p, err)
	}
	return launchAndConfirm(directory, p)
}

func launchAndConfirm(directory string, p *Plan) error {
	cmd, err := startApplicationForUpdate(p.Target)
	if err != nil {
		if restoreErr := restoreInstallation(p); restoreErr != nil {
			return failPlan(directory, p, fmt.Errorf("启动失败，旧版本备份在 %s: %w", p.Backup, restoreErr))
		}
		return failPlan(directory, p, err)
	}
	defer stopLaunchMonitor(cmd)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait(); close(exited) }()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := waitApplicationStartup(ctx, directory, p, exited); err != nil {
		if errors.Is(err, errApplicationExited) {
			if restoreErr := restoreInstallation(p); restoreErr != nil {
				return failPlan(directory, p, fmt.Errorf("新版启动失败，旧版备份在 %s: %w", p.Backup, restoreErr))
			}
			return failPlan(directory, p, err)
		}
		// A successful OS launch request is not proof that the new window opened.
		// Keep the backup when startup is unconfirmed; a late/next successful
		// startup can still acknowledge the exact installed version and hash.
		p.Error = "新版未能确认启动，请重新打开软件；旧版备份已保留"
		_ = WritePlan(directory, p)
		return fmt.Errorf("%s: %w", p.Error, err)
	}
	_ = os.Remove(filepath.Join(directory, "opened.json"))
	return nil
}

func waitApplicationOpened(ctx context.Context, directory string, p *Plan) error {
	return waitApplicationStartup(ctx, directory, p, nil)
}

var errApplicationExited = errors.New("新版在启动完成前退出，已恢复旧版")

func hasOpenedReceipt(directory string, p *Plan) bool {
	var receipt openedInstallation
	raw, err := os.ReadFile(filepath.Join(directory, "opened.json"))
	return err == nil && json.Unmarshal(raw, &receipt) == nil && receipt.Token == p.Token && receipt.Version == p.Version && receipt.BinaryHash == p.BinaryHash
}

func waitApplicationStartup(ctx context.Context, directory string, p *Plan, exited <-chan error) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if hasOpenedReceipt(directory, p) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-exited:
			// A short-lived test/headless app may acknowledge and then exit
			// between polls. The receipt still proves a successful startup.
			if hasOpenedReceipt(directory, p) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("%w: %v", errApplicationExited, err)
			}
			return errApplicationExited
		case <-ticker.C:
		}
	}
}

func waitInstallerLock(directory string) (func(), error) {
	deadline := time.Now().Add(3 * time.Second)
	for {
		unlock, err := lockInstaller(directory)
		if err == nil {
			return unlock, nil
		}
		if !errors.Is(err, errInstallBusy) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

var errInstallBusy = errors.New("更新正在安装中，请稍后再试")

func targetBinary(p *Plan) string {
	if p.BinaryRelative == "" {
		return p.Target
	}
	return filepath.Join(p.Target, p.BinaryRelative)
}

func failPlan(directory string, p *Plan, err error) error {
	p.Phase = "failed"
	p.AutoApply = false
	p.Error = "更新未能安装，当前版本仍可使用，可以重试：" + err.Error()
	_ = WritePlan(directory, p)
	// If startup scheduled the installation, reopen the retained old app once.
	_ = launchApplication(p.Target)
	return err
}

func hashFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("update executable must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(source, target string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		_ = out.Close()
		if !complete {
			_ = os.Remove(target)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}

func checkExecutableVersion(ctx context.Context, path, expected string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--bbdown-version")
	quietCommand(cmd)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("更新程序无法运行: %w", err)
	}
	if strings.TrimSpace(string(out)) != expected {
		return errors.New("安装包版本与后台发布版本不一致")
	}
	return nil
}
