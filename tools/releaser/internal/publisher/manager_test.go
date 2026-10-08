package publisher

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type memoryTokens map[string]string

func (s memoryTokens) Get(base string) (string, error) {
	v, ok := s[base]
	if !ok {
		return "", errors.New("missing")
	}
	return v, nil
}
func (s memoryTokens) Set(base, token string) error { s[base] = token; return nil }
func (s memoryTokens) Delete(base string) error     { delete(s, base); return nil }
func testToken(exp time.Time) string {
	b, _ := json.Marshal(map[string]any{"exp": exp.Unix()})
	return "eyJ0eXAiOiJKV1QifQ." + base64.RawURLEncoding.EncodeToString(b) + ".c2lnbmF0dXJl"
}
func fixture(t *testing.T) *Manager {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("publisher currently runs on macOS")
	}
	root := t.TempDir()
	binaries := filepath.Join(root, "bin")
	_ = os.MkdirAll(binaries, 0700)
	for _, name := range []string{"gh", "uv", "google-chrome"} {
		if e := os.WriteFile(filepath.Join(binaries, name), []byte("#!/bin/sh\nexit 0\n"), 0700); e != nil {
			t.Fatal(e)
		}
	}
	t.Setenv("PATH", binaries+string(os.PathListSeparator)+os.Getenv("PATH"))
	for name, content := range map[string]string{"wails.json": `{"info":{"productVersion":"1.1.12"}}`, "scripts/publish_release.py": "# fixture\n", "scripts/publish.example.json": `{"github_repo":"example/repo","gitcode_repo":"example/repo","lanzou":{"folder_path":["test"],"folder_share_url":"https://example.com/downloads"},"updater":{"enabled":false}}`} {
		f := filepath.Join(root, name)
		_ = os.MkdirAll(filepath.Dir(f), 0700)
		if e := os.WriteFile(f, []byte(content), 0600); e != nil {
			t.Fatal(e)
		}
	}
	git := func(args ...string) {
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Dir = root
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git fixture: %v %s", e, b)
		}
	}
	git("init", "-q")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.com")
	git("add", "wails.json", "scripts")
	git("commit", "-qm", "fixture")
	git("remote", "add", "gitcode", "https://example.com/repo.git")
	for _, marker := range []string{"login-ready", "gitcode-login-ready"} {
		p := filepath.Join(root, ".local/publish/lanzou-profile", marker)
		_ = os.MkdirAll(filepath.Dir(p), 0700)
		_ = os.WriteFile(p, nil, 0600)
	}
	// All private config/cache/tool stubs are deliberately untracked and ignored.
	git("config", "core.excludesFile", filepath.Join(root, "excludes"))
	_ = os.WriteFile(filepath.Join(root, "excludes"), []byte("bin/\n.local/\nexcludes\nsettings.json\n"), 0600)
	m := New(root, filepath.Join(root, "settings.json"), memoryTokens{})
	return m
}
func awaitIdle(t *testing.T, m *Manager) State {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s := m.GetState()
		if !s.Busy {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("task did not finish")
	return State{}
}
func fakeUV(t *testing.T, body string) {
	t.Helper()
	if e := os.WriteFile(tool("uv"), []byte("#!/bin/sh\n"+body), 0700); e != nil {
		t.Fatal(e)
	}
}
func allEvents() string {
	s := ""
	for _, id := range []string{"build", "assets", "gitcode", "lanzou"} {
		s += fmt.Sprintf("printf '%%s\\n' 'BBDOWN_EVENT {\"protocol\":1,\"step\":\"%s\",\"status\":\"complete\",\"message\":\"verified\"}'\n", id)
	}
	return s
}
func TestJobIsSavedBeforeLaunchAndRestoredWithoutStarting(t *testing.T) {
	m := fixture(t)
	fakeUV(t, allEvents()+"sleep 1\n")
	if e := m.Start("更新说明", false); e != nil {
		t.Fatal(e)
	}
	s := m.GetState()
	if !s.Busy || s.Job.Commit == "" || s.Job.Notes != "更新说明" {
		t.Fatal("identity not fixed")
	}
	if e := m.Start("different", false); e == nil {
		t.Fatal("concurrent task accepted")
	}
	var recorded Job
	if e := readJSON(jobFile(s.Settings.Project, "1.1.12"), &recorded); e != nil || recorded.Commit != s.Job.Commit {
		t.Fatal("job missing before completion")
	}
	end := awaitIdle(t, m)
	if end.Job.Status != "complete" {
		t.Fatal(end.Job.Error)
	}
	copy := New(s.Settings.Project, m.prefs, memoryTokens{})
	loaded := copy.GetState()
	if loaded.Busy || loaded.Job.Status != "complete" || len(loaded.Job.Logs) == 0 {
		t.Fatal("restoring changed completed task or lost logs")
	}
	if info, e := os.Stat(jobFile(s.Settings.Project, "1.1.12")); e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("job permissions")
	}
}
func TestZeroExitWithoutConfirmedStepsIsNotSuccess(t *testing.T) {
	m := fixture(t)
	if e := m.Start("", false); e != nil {
		t.Fatal(e)
	}
	if s := awaitIdle(t, m); s.Job.Status != "failed" {
		t.Fatal("unconfirmed release reported successful")
	}
}
func TestCancellationPreservesProgressAndStopsOwnedProcess(t *testing.T) {
	m := fixture(t)
	fakeUV(t, "printf '%s\\n' 'BBDOWN_EVENT {\"protocol\":1,\"step\":\"build\",\"status\":\"running\",\"message\":\"waiting\"}'\ntrap 'exit 130' INT\nsleep 30 &\nwait\n")
	if e := m.Start("", false); e != nil {
		t.Fatal(e)
	}
	time.Sleep(80 * time.Millisecond)
	m.Shutdown()
	s := awaitIdle(t, m)
	if s.Job.Status != "stopped" || !completed(s.Job, "preflight") {
		t.Fatal("cancel discarded boundary")
	}
}
func TestResumeKeepsOriginalCommitAfterCurrentVersionChanges(t *testing.T) {
	m := fixture(t)
	fakeUV(t, allEvents())
	if e := m.Start("original notes", false); e != nil {
		t.Fatal(e)
	}
	s := awaitIdle(t, m)
	cmd := exec.Command("/usr/bin/git", "tag", "v1.1.12", s.Job.Commit)
	cmd.Dir = s.Settings.Project
	if e := cmd.Run(); e != nil {
		t.Fatal(e)
	}
	_ = os.WriteFile(filepath.Join(s.Settings.Project, "wails.json"), []byte(`{"info":{"productVersion":"1.1.20"}}`), 0600)
	fakeUV(t, "printf '%s\\n' \"$@\" > .local/args.txt\n"+allEvents())
	if e := m.Start("must not replace original", true); e != nil {
		t.Fatal(e)
	}
	end := awaitIdle(t, m)
	args, _ := os.ReadFile(filepath.Join(s.Settings.Project, ".local/args.txt"))
	if end.Job.Version != "1.1.12" || end.Job.Commit != s.Job.Commit || end.Job.Notes != "original notes" || !strings.Contains(string(args), "--resume") || !strings.Contains(string(args), s.Job.Commit) {
		t.Fatal("resume did not preserve identity")
	}
}
func TestResumeRejectsTagMovedToAnotherCommit(t *testing.T) {
	m := fixture(t)
	if e := m.Start("", false); e != nil {
		t.Fatal(e)
	}
	s := awaitIdle(t, m)
	for _, args := range [][]string{{"commit", "--allow-empty", "-qm", "changed"}, {"tag", "v1.1.12"}} {
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Dir = s.Settings.Project
		if e := cmd.Run(); e != nil {
			t.Fatal(e)
		}
	}
	if e := m.Start("", true); e == nil || !strings.Contains(e.Error(), "标签已改变") {
		t.Fatal("moved tag accepted")
	}
}
func TestInterruptedRestorationRetainsLogs(t *testing.T) {
	m := fixture(t)
	j := Job{Version: "1.1.12", Project: m.state.Settings.Project, Status: "running", Updated: stamp(), Steps: newSteps(false), Logs: []Entry{{stamp(), "build", "saved progress"}}}
	if e := writeJSON(jobFile(j.Project, j.Version), j); e != nil {
		t.Fatal(e)
	}
	s := New(j.Project, m.prefs, memoryTokens{}).GetState()
	if s.Busy || s.Job.Status != "interrupted" || len(s.Job.Logs) != 1 {
		t.Fatal("interrupted job not recoverable")
	}
}
func TestSettingsPreserveFolderAndNeverPersistPasswordOrToken(t *testing.T) {
	m := fixture(t)
	s := m.GetState().Settings
	s.CloudEnabled = true
	s.CloudURL = "https://example.com/"
	if e := m.Configure(s); e != nil {
		t.Fatal(e)
	}
	var cfg map[string]any
	_ = readJSON(m.configFile(), &cfg)
	if cfg["lanzou"].(map[string]any)["folder_share_url"] != "https://example.com/downloads" {
		t.Fatal("channel config lost")
	}
	s.CloudURL = "https://user:secret@example.com"
	if m.Configure(s) == nil {
		t.Fatal("credential-bearing URL accepted")
	}
}
func TestCloudLoginBusinessErrorsAndSuccess(t *testing.T) {
	m := fixture(t)
	token := testToken(time.Now().Add(time.Hour))
	success := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/admin/login" {
			t.Error("wrong login contract")
		}
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["password"] != "private-password" {
			t.Error("credentials not sent correctly")
		}
		if success {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]string{"token": token}})
		} else {
			fmt.Fprint(w, `{"success":false,"message":"private-password"}`)
		}
	}))
	defer server.Close()
	m.client = server.Client()
	if e := m.LoginCloud(server.URL, "admin", "private-password"); e == nil || strings.Contains(e.Error(), "private-password") {
		t.Fatal("business error or credentials leaked")
	}
	success = true
	if e := m.LoginCloud(server.URL, "admin", "private-password"); e != nil {
		t.Fatal(e)
	}
	for _, f := range []string{m.prefs, m.configFile()} {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), token) || strings.Contains(string(b), "private-password") {
			t.Fatal("secret written to config")
		}
	}
	if s := m.GetState(); !s.CloudLoggedIn || !s.Settings.CloudEnabled {
		t.Fatal("successful login did not enable integration")
	}
}
func TestProgressRedactsTokensAndRejectsSignedLinks(t *testing.T) {
	m := fixture(t)
	m.state.Job = &Job{Version: "1.1.12", Project: m.state.Settings.Project, Steps: newSteps(true)}
	token := testToken(time.Now().Add(time.Hour))
	m.secret = "sensitive-value"
	m.consume(`BBDOWN_EVENT {"protocol":1,"step":"gitcode","status":"running","message":"token=sensitive-value `+token+`","link":"https://example.com/file?token=secret"}`, true)
	s := m.GetState()
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "sensitive-value") || strings.Contains(string(b), token) || s.Job.Steps[3].Link != "" {
		t.Fatal("sensitive progress exposed")
	}
	if tokenValid(testToken(time.Now().Add(-time.Hour))) {
		t.Fatal("expired token accepted")
	}
}
