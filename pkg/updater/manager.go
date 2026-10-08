package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bilibili_downloader/pkg/update"
	"bilibili_downloader/pkg/utils"
)

const Week = 7 * 24 * time.Hour

// State is a small UI projection. Network errors never interrupt the main app.
type State struct {
	Revision       uint64 `json:"revision"`
	Checked        bool   `json:"checked"`
	AutoCheck      bool   `json:"autoCheck"`
	CurrentVersion string `json:"currentVersion"`
	Version        string `json:"version"`
	Notes          string `json:"notes"`
	Phase          string `json:"phase"`
	Error          string `json:"error"`
	Downloaded     int64  `json:"downloaded"`
	Total          int64  `json:"total"`
	HasUpdate      bool   `json:"hasUpdate"`
}

type savedState struct {
	AutoCheck       bool            `json:"autoCheck"`
	LastCheck       int64           `json:"lastCheck"`
	RetryUntil      int64           `json:"retryUntil"`
	Release         *update.Release `json:"release,omitempty"`
	PendingDownload bool            `json:"pendingDownload"`
	Downloaded      int64           `json:"downloaded"`
}

type Client interface {
	Check(context.Context) (*update.CheckResult, error)
	Download(context.Context, update.Release, update.DownloadOptions) (string, error)
	Confirm(context.Context, update.Release) error
}

type Config struct {
	Directory  string
	Version    string
	Executable string
	Client     Client
	Now        func() time.Time
}

type Manager struct {
	mu       sync.Mutex
	cfg      Config
	saved    savedState
	state    State
	callback func(State)
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	closed   bool
	prepare  func(context.Context, string, update.Release, Config) (*Plan, error)
}

func New(cfg Config) (*Manager, error) {
	if cfg.Client == nil || cfg.Directory == "" || cfg.Executable == "" {
		return nil, errors.New("invalid updater configuration")
	}
	version, _, err := update.ParseVersion(cfg.Version)
	if err != nil {
		return nil, err
	}
	cfg.Version = version
	cfg.Directory, err = filepath.Abs(cfg.Directory)
	if err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if err := os.MkdirAll(cfg.Directory, 0700); err != nil {
		return nil, err
	}
	m := &Manager{cfg: cfg, saved: savedState{AutoCheck: true}, prepare: Prepare}
	if raw, err := os.ReadFile(m.statePath()); err == nil {
		if json.Unmarshal(raw, &m.saved) != nil {
			m.saved = savedState{AutoCheck: true}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	m.state = State{AutoCheck: m.saved.AutoCheck, CurrentVersion: cfg.Version, Phase: "idle"}
	if r := m.saved.Release; r != nil {
		if newerVersion(r.Version, cfg.Version) {
			m.state.HasUpdate = true
			m.state.Version = r.Version
			m.state.Notes = r.Notes
			m.state.Total = r.Artifact.Size
			m.state.Phase = "available"
			if m.saved.PendingDownload {
				m.state.Phase = "paused"
				m.state.Downloaded = max(0, min(m.saved.Downloaded, r.Artifact.Size))
			}
		} else {
			m.saved.Release = nil
		}
	}
	if p, err := ReadPlan(cfg.Directory); err == nil {
		target, targetErr := installationTarget(cfg.Executable)
		if validatePlan(p, cfg.Directory) == nil && targetErr == nil && target == p.Target && newerVersion(p.Version, cfg.Version) {
			if hash, err := hashFile(preparedBinary(p)); err == nil && hash == p.BinaryHash {
				m.state.Version = p.Version
				m.state.HasUpdate = true
				m.state.Phase = "ready"
				m.state.Error = p.Error
				if p.Phase != "ready" {
					m.state.Phase = "install_failed"
					if m.state.Error == "" {
						m.state.Error = "上次更新未完成，可以重试安装"
					}
				}
			} else {
				m.state.Error = "更新文件无法使用，请重新下载"
			}
		} else if p.Version != cfg.Version {
			m.state.Error = "软件位置或更新文件已改变，请重新下载"
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		m.state.Error = "更新记录无法读取，请重新检查更新"
	}
	return m, nil
}

func (m *Manager) statePath() string { return filepath.Join(m.cfg.Directory, "state.json") }
func (m *Manager) saveLocked() error {
	raw, err := json.Marshal(m.saved)
	if err != nil {
		return err
	}
	return utils.AtomicWriteFile(m.statePath(), raw, 0600)
}
func (m *Manager) Snapshot() State { m.mu.Lock(); defer m.mu.Unlock(); return m.state }
func (m *Manager) SetCallback(callback func(State)) {
	m.mu.Lock()
	m.callback = callback
	m.mu.Unlock()
}
func (m *Manager) publish() {
	m.mu.Lock()
	m.state.Revision++
	state, callback := m.state, m.callback
	m.mu.Unlock()
	if callback != nil {
		callback(state)
	}
}

func (m *Manager) SetAutoCheck(enabled bool) error {
	m.mu.Lock()
	previous := m.saved.AutoCheck
	m.saved.AutoCheck = enabled
	if err := m.saveLocked(); err != nil {
		m.saved.AutoCheck = previous
		m.mu.Unlock()
		return err
	}
	m.state.AutoCheck = enabled
	m.mu.Unlock()
	m.publish()
	return nil
}

// Called only after startup. There is no timer or background periodic loop.
func (m *Manager) CheckOnStartup(ctx context.Context) { _ = m.check(ctx, true) }
func (m *Manager) Check(ctx context.Context) error    { return m.check(ctx, false) }
func (m *Manager) check(ctx context.Context, automatic bool) error {
	m.mu.Lock()
	now := m.cfg.Now()
	if m.closed || m.cancel != nil || m.state.Phase == "ready" || m.state.Phase == "restarting" {
		m.mu.Unlock()
		return nil
	}
	// A backwards clock adjustment must not disable checks indefinitely.
	if m.saved.LastCheck > now.Unix() {
		m.saved.LastCheck = 0
	}
	if automatic && (!m.saved.AutoCheck || (m.saved.LastCheck > 0 && now.Sub(time.Unix(m.saved.LastCheck, 0)) < Week)) {
		m.mu.Unlock()
		return nil
	}
	if now.Unix() < m.saved.RetryUntil {
		m.mu.Unlock()
		return errors.New("请求较频繁，请稍后再检查")
	}
	previous := m.saved.LastCheck
	m.saved.LastCheck = now.Unix()
	if err := m.saveLocked(); err != nil {
		m.saved.LastCheck = previous
		m.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.state.Phase = "checking"
	m.state.Checked = false
	m.state.Error = ""
	m.wg.Add(1)
	m.mu.Unlock()
	m.publish()
	go func() {
		defer m.wg.Done()
		defer cancel()
		result, err := m.cfg.Client.Check(ctx)
		m.mu.Lock()
		m.cancel = nil
		if err != nil {
			var api *update.APIError
			if errors.As(err, &api) && api.RetryAfter > 0 {
				m.saved.RetryUntil = m.cfg.Now().Add(api.RetryAfter).Unix()
			}
			m.state.Error = friendlyError(err)
			if m.state.HasUpdate {
				m.state.Phase = "available"
			} else {
				m.state.Phase = "idle"
			}
		} else if result != nil {
			m.state.Checked = true
			oldRelease := m.saved.Release
			oldPending := m.saved.PendingDownload
			oldDownloaded := m.saved.Downloaded
			m.saved.RetryUntil = 0
			m.saved.Release = result.Release
			m.state.HasUpdate = result.HasUpdate
			m.state.Phase = "idle"
			m.state.Version = ""
			m.state.Notes = ""
			m.state.Downloaded = 0
			m.state.Total = 0
			m.saved.PendingDownload = false
			m.saved.Downloaded = 0
			if result.HasUpdate && result.Release != nil {
				m.state.Version = result.Release.Version
				m.state.Notes = result.Release.Notes
				m.state.Total = result.Release.Artifact.Size
				m.state.Phase = "available"
				if oldPending && oldRelease != nil && oldRelease.Version == result.Release.Version && strings.EqualFold(oldRelease.Artifact.SHA256, result.Release.Artifact.SHA256) {
					m.saved.PendingDownload = true
					m.saved.Downloaded = oldDownloaded
					m.state.Downloaded = max(0, min(oldDownloaded, m.state.Total))
					m.state.Phase = "paused"
				}
			}
		} else {
			m.state.Phase = "idle"
			m.state.Error = "检查未完成，请重试"
		}
		if saveErr := m.saveLocked(); saveErr != nil {
			m.state.Error = "检查结果未能保存，请重试"
		}
		m.mu.Unlock()
		m.publish()
	}()
	return nil
}

func (m *Manager) Download(ctx context.Context) error {
	m.mu.Lock()
	if m.closed || m.cancel != nil || m.state.Phase == "restarting" {
		m.mu.Unlock()
		return errors.New("更新正在处理中")
	}
	if m.state.Phase == "ready" {
		m.mu.Unlock()
		return nil
	}
	if !m.state.HasUpdate || m.saved.Release == nil {
		m.mu.Unlock()
		return errors.New("请先检查更新")
	}
	m.saved.PendingDownload = true
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	release := *m.saved.Release
	ctx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.state.Phase = "downloading"
	m.state.Error = ""
	m.wg.Add(1)
	m.mu.Unlock()
	m.publish()
	go func() {
		defer m.wg.Done()
		defer cancel()
		path, err := m.cfg.Client.Download(ctx, release, update.DownloadOptions{Directory: filepath.Join(m.cfg.Directory, "downloads"), Resume: true, OnProgress: func(p update.Progress) {
			m.mu.Lock()
			m.state.Downloaded = p.Downloaded
			m.saved.Downloaded = p.Downloaded
			m.state.Total = p.Total
			m.mu.Unlock()
			m.publish()
		}})
		var plan *Plan
		if err == nil {
			err = m.cfg.Client.Confirm(ctx, release)
		}
		if err == nil {
			m.mu.Lock()
			m.state.Phase = "preparing"
			m.mu.Unlock()
			m.publish()
			plan, err = m.prepare(ctx, path, release, m.cfg)
		}
		if err == nil {
			err = WritePlan(m.cfg.Directory, plan)
		}
		if err != nil && plan != nil {
			_ = os.RemoveAll(plan.Prepared)
		}
		m.mu.Lock()
		m.cancel = nil
		switch {
		case err == nil:
			m.saved.PendingDownload = false
			m.state.Phase = "ready"
			m.state.Downloaded = release.Artifact.Size
			m.state.Error = ""
		case errors.Is(err, context.Canceled):
			m.state.Phase = "paused"
			m.state.Error = ""
		case errors.Is(err, update.ErrReleaseChanged):
			m.saved.PendingDownload = false
			m.saved.Downloaded = 0
			m.state.Phase = "idle"
			m.state.Error = "此版本已撤回或有更新版本，请重新检查"
			m.state.HasUpdate = false
			m.saved.Release = nil
		default:
			m.state.Phase = "paused"
			m.state.Error = friendlyError(err)
		}
		var api *update.APIError
		if errors.As(err, &api) && api.RetryAfter > 0 {
			m.saved.RetryUntil = m.cfg.Now().Add(api.RetryAfter).Unix()
		}
		if saveErr := m.saveLocked(); saveErr != nil {
			log.Printf("save update state: %v", saveErr)
		}
		m.mu.Unlock()
		m.publish()
	}()
	return nil
}

func (m *Manager) Pause() {
	m.mu.Lock()
	if m.cancel != nil && m.state.Phase == "downloading" {
		m.cancel()
	}
	m.mu.Unlock()
}
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// MarkOpened is called only when the new app's window is ready. Until then the
// previous version's backup is kept next to the installation.
func (m *Manager) MarkOpened() {
	p, err := ReadPlan(m.cfg.Directory)
	if err != nil || p.Version != m.cfg.Version || validatePlan(p, m.cfg.Directory) != nil {
		return
	}
	target, err := installationTarget(m.cfg.Executable)
	if err != nil || target != p.Target {
		return
	}
	hash, err := hashFile(targetBinary(p))
	if err != nil || hash != p.BinaryHash {
		return
	}
	// Also recovers a helper interrupted after atomic replacement but before
	// saving the "applied" marker. The running version and hash are authoritative.
	if p.Phase != "applied" && p.Phase != "scheduled" {
		return
	}
	receipt, err := json.Marshal(openedInstallation{Token: p.Token, Version: p.Version, BinaryHash: p.BinaryHash})
	if err != nil {
		return
	}
	if err := utils.AtomicWriteFile(filepath.Join(m.cfg.Directory, "opened.json"), receipt, 0600); err != nil {
		log.Printf("acknowledge update startup: %v", err)
		return
	}
	_ = os.RemoveAll(p.Backup)
	_ = os.Remove(filepath.Join(m.cfg.Directory, "install.json"))
	_ = os.RemoveAll(filepath.Join(m.cfg.Directory, "downloads"))
	_ = os.RemoveAll(filepath.Join(m.cfg.Directory, "helper"))
	m.mu.Lock()
	m.saved.Release = nil
	m.saved.PendingDownload = false
	m.saved.Downloaded = 0
	m.state.HasUpdate = false
	m.state.Phase = "idle"
	m.state.Version = ""
	m.state.Error = ""
	_ = m.saveLocked()
	m.mu.Unlock()
	m.publish()
}

func (m *Manager) LaunchInstall() error {
	m.mu.Lock()
	if m.closed || m.cancel != nil || (m.state.Phase != "ready" && m.state.Phase != "install_failed") {
		m.mu.Unlock()
		return errors.New("更新尚未准备完成")
	}
	m.state.Phase = "restarting"
	m.state.Error = ""
	m.mu.Unlock()
	m.publish()
	err := LaunchInstall(m.cfg.Directory, m.cfg.Executable)
	if err != nil {
		m.mu.Lock()
		m.state.Phase = "install_failed"
		m.state.Error = friendlyError(err)
		m.mu.Unlock()
		m.publish()
	}
	return err
}

// Reprepare from the verified cache. This also recovers from moving the app,
// deleting a staged bundle, or a previous failed helper; it never deletes old.
func (m *Manager) RetryDownload(ctx context.Context) error {
	m.mu.Lock()
	if m.closed || m.cancel != nil || m.state.Phase == "restarting" {
		m.mu.Unlock()
		return errors.New("更新正在处理中")
	}
	m.mu.Unlock()
	unlock, err := lockInstaller(m.cfg.Directory)
	if err != nil {
		return err
	}
	defer unlock()
	if p, err := ReadPlan(m.cfg.Directory); err == nil && validatePlan(p, m.cfg.Directory) == nil {
		if hash, e := hashFile(preparedBinary(p)); e == nil && hash == p.BinaryHash {
			_ = os.RemoveAll(p.Prepared)
		}
	}
	if err := os.Remove(filepath.Join(m.cfg.Directory, "install.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	m.mu.Lock()
	m.state.Phase = "available"
	m.state.Error = ""
	m.mu.Unlock()
	return m.Download(ctx)
}

func newerVersion(candidate, current string) bool {
	_, a, e1 := update.ParseVersion(candidate)
	_, b, e2 := update.ParseVersion(current)
	if e1 != nil || e2 != nil {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func friendlyError(err error) string {
	if errors.Is(err, update.ErrRateLimited) {
		return "请求较频繁，请稍后再试"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "连接超时，请稍后再试"
	}
	if errors.Is(err, context.Canceled) {
		return "下载已暂停"
	}
	log.Printf("updater: %v", err)
	if errors.Is(err, os.ErrPermission) {
		return "无法保存更新，请确认软件所在目录可写后重试"
	}
	// Installation errors are already actionable; transport errors should not
	// expose URLs or implementation details in the customer's settings screen.
	if strings.Contains(err.Error(), "http") || strings.Contains(err.Error(), "source ") {
		return "下载暂时无法完成，请检查网络后继续更新"
	}
	return fmt.Sprintf("更新未完成，可以重试：%v", err)
}
