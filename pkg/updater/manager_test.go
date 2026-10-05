package updater

import (
	"bilibili_downloader/pkg/update"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClient struct {
	checks   atomic.Int32
	check    func(context.Context) (*update.CheckResult, error)
	download func(context.Context, update.Release, update.DownloadOptions) (string, error)
	confirm  func(context.Context, update.Release) error
}

func (c *fakeClient) Check(ctx context.Context) (*update.CheckResult, error) {
	c.checks.Add(1)
	if c.check != nil {
		return c.check(ctx)
	}
	return &update.CheckResult{}, nil
}
func (c *fakeClient) Download(ctx context.Context, r update.Release, o update.DownloadOptions) (string, error) {
	return c.download(ctx, r, o)
}
func (c *fakeClient) Confirm(ctx context.Context, r update.Release) error {
	if c.confirm != nil {
		return c.confirm(ctx, r)
	}
	return nil
}
func newTestManager(t *testing.T, c *fakeClient, now func() time.Time) *Manager {
	t.Helper()
	m, err := New(Config{Directory: t.TempDir(), Executable: filepath.Join(t.TempDir(), "BBDown Pro.exe"), Version: "1.0.0", Client: c, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}
func TestWeeklyStartupPolicyAndManualOverride(t *testing.T) {
	now := time.Unix(1800000000, 0)
	c := &fakeClient{}
	m := newTestManager(t, c, func() time.Time { return now })
	if !m.Snapshot().AutoCheck {
		t.Fatal("automatic checks should default to enabled")
	}
	m.CheckOnStartup(context.Background())
	m.wg.Wait()
	now = now.Add(Week - time.Second)
	m.CheckOnStartup(context.Background())
	m.wg.Wait()
	if c.checks.Load() != 1 {
		t.Fatal("checked before a week")
	}
	now = now.Add(time.Second)
	m.CheckOnStartup(context.Background())
	m.wg.Wait()
	if c.checks.Load() != 2 {
		t.Fatal("did not check at one week")
	}
	if err := m.SetAutoCheck(false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(Week)
	m.CheckOnStartup(context.Background())
	m.wg.Wait()
	if c.checks.Load() != 2 {
		t.Fatal("checked while disabled")
	}
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.wg.Wait()
	if c.checks.Load() != 3 {
		t.Fatal("manual check disabled by policy")
	}
	// Restart reads both the choice and attempt timestamp from disk.
	restarted, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.Snapshot().AutoCheck {
		t.Fatal("choice not persisted")
	}
	if err := restarted.SetAutoCheck(true); err != nil {
		t.Fatal(err)
	}
	restarted.CheckOnStartup(context.Background())
	restarted.wg.Wait()
	if c.checks.Load() != 3 {
		t.Fatal("restart repeated the same week's check")
	}
}
func TestFailedCheckAndRateLimitSurviveRestart(t *testing.T) {
	now := time.Unix(1800000000, 0)
	c := &fakeClient{check: func(context.Context) (*update.CheckResult, error) {
		return nil, &update.APIError{StatusCode: 429, RetryAfter: time.Hour}
	}}
	m := newTestManager(t, c, func() time.Time { return now })
	m.CheckOnStartup(context.Background())
	m.wg.Wait()
	restarted, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := restarted.Check(context.Background()); err == nil {
		t.Fatal("429 cooldown lost")
	}
	now = now.Add(time.Hour)
	restarted.CheckOnStartup(context.Background())
	restarted.wg.Wait()
	if c.checks.Load() != 1 {
		t.Fatal("automatic error caused a startup request loop")
	}
	now = now.Add(Week)
	restarted.CheckOnStartup(context.Background())
	restarted.wg.Wait()
	if c.checks.Load() != 2 {
		t.Fatal("never recovered from failed check")
	}
}
func offeredRelease() update.Release {
	return update.Release{Version: "1.1.0", Notes: "test notes", Artifact: update.Artifact{Size: 100}}
}
func offer(c *fakeClient) {
	c.check = func(context.Context) (*update.CheckResult, error) {
		r := offeredRelease()
		return &update.CheckResult{HasUpdate: true, Release: &r}, nil
	}
}
func TestPauseResumeAndPrepareOnlyAfterConfirmation(t *testing.T) {
	c := &fakeClient{}
	offer(c)
	started := make(chan struct{}, 1)
	attempts := 0
	confirmed := false
	c.download = func(ctx context.Context, r update.Release, o update.DownloadOptions) (string, error) {
		if !o.Resume {
			t.Error("resume was not requested")
		}
		attempts++
		o.OnProgress(update.Progress{Downloaded: 35, Total: 100})
		if attempts == 1 {
			started <- struct{}{}
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "verified.zip", nil
	}
	c.confirm = func(context.Context, update.Release) error { confirmed = true; return nil }
	m := newTestManager(t, c, nil)
	m.prepare = func(context.Context, string, update.Release, Config) (*Plan, error) {
		if !confirmed {
			t.Error("prepared before confirming release")
		}
		return &Plan{Version: "1.1.0"}, nil
	}
	_ = m.Check(context.Background())
	m.wg.Wait()
	_ = m.Download(context.Background())
	<-started
	if err := m.Download(context.Background()); err == nil {
		t.Error("parallel download allowed")
	}
	m.Pause()
	m.wg.Wait()
	if m.Snapshot().Phase != "paused" || m.Snapshot().Downloaded != 35 {
		t.Fatal(m.Snapshot())
	}
	reopened, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if state := reopened.Snapshot(); state.Phase != "paused" || state.Downloaded != 35 {
		t.Fatal("paused update state did not survive restart", state)
	}
	reopened.Close()
	_ = m.Download(context.Background())
	m.wg.Wait()
	if m.Snapshot().Phase != "ready" || m.Snapshot().Downloaded != 100 {
		t.Fatal(m.Snapshot())
	}
	if _, err := os.Stat(filepath.Join(m.cfg.Directory, "install.json")); err != nil {
		t.Fatal("ready update not persisted", err)
	}
}
func TestWithdrawnAndFailedPreparationAreRetryable(t *testing.T) {
	c := &fakeClient{}
	offer(c)
	c.download = func(context.Context, update.Release, update.DownloadOptions) (string, error) { return "package", nil }
	c.confirm = func(context.Context, update.Release) error { return update.ErrReleaseChanged }
	m := newTestManager(t, c, nil)
	prepared := false
	m.prepare = func(context.Context, string, update.Release, Config) (*Plan, error) {
		prepared = true
		return nil, errors.New("bad package")
	}
	_ = m.Check(context.Background())
	m.wg.Wait()
	_ = m.Download(context.Background())
	m.wg.Wait()
	if prepared || m.Snapshot().HasUpdate {
		t.Fatal("withdrawn package was prepared")
	}
	c.confirm = nil
	_ = m.Check(context.Background())
	m.wg.Wait()
	_ = m.Download(context.Background())
	m.wg.Wait()
	if m.Snapshot().Phase != "paused" {
		t.Fatal(m.Snapshot())
	}
	m.prepare = func(context.Context, string, update.Release, Config) (*Plan, error) {
		return &Plan{Version: "1.1.0"}, nil
	}
	_ = m.Download(context.Background())
	m.wg.Wait()
	if m.Snapshot().Phase != "ready" {
		t.Fatal("preparation error permanently blocked updates")
	}
}
func TestCloseCancelsActiveDownload(t *testing.T) {
	c := &fakeClient{}
	offer(c)
	started := make(chan struct{})
	c.download = func(ctx context.Context, _ update.Release, _ update.DownloadOptions) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	m := newTestManager(t, c, nil)
	_ = m.Check(context.Background())
	m.wg.Wait()
	_ = m.Download(context.Background())
	<-started
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown hung")
	}
}
