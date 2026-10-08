package publisher

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
var jwtPattern = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
var querySecret = regexp.MustCompile(`(?i)(access_token|token|signature|password|authorization)(=|:)[^\s&]+`)

type Manager struct {
	mu     sync.Mutex
	state  State
	prefs  string
	store  TokenStore
	cancel context.CancelFunc
	secret string
	client *http.Client
}

func New(defaultProject, prefs string, store TokenStore) *Manager {
	m := &Manager{prefs: prefs, store: store, client: &http.Client{Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	m.state.Settings = Settings{Project: defaultProject, CloudURL: DefaultCloud, VisibleBrowser: true}
	_ = readJSON(prefs, &m.state.Settings)
	if m.state.Settings.CloudURL == "" {
		m.state.Settings.CloudURL = DefaultCloud
	}
	if validProject(m.state.Settings.Project) == nil {
		_ = m.ensureConfig()
		var cfg map[string]any
		if readJSON(m.configFile(), &cfg) == nil {
			if u, ok := cfg["updater"].(map[string]any); ok {
				if v, ok := u["base_url"].(string); ok && v != "" {
					m.state.Settings.CloudURL = v
				}
				if v, ok := u["enabled"].(bool); ok {
					m.state.Settings.CloudEnabled = v
				}
			}
		}
		m.loadHistoryLocked()
		if m.state.Settings.LastVersion != "" {
			for _, j := range m.state.History {
				if j.Version == m.state.Settings.LastVersion {
					var restored Job
					if readJSON(jobFile(j.Project, j.Version), &restored) == nil {
						m.state.Job = &restored
					}
					break
				}
			}
		}
		if m.state.Job == nil && len(m.state.History) > 0 {
			var restored Job
			latest := m.state.History[0]
			if readJSON(jobFile(latest.Project, latest.Version), &restored) == nil {
				m.state.Job = &restored
			}
		}
		if m.state.Job != nil && m.state.Job.Status == "running" {
			m.state.Job.Status = "interrupted"
			m.state.Job.Error = "上次任务中断，已完成步骤保留；点击继续发布"
			_ = m.persistLocked()
		}
	}
	return m
}
func (m *Manager) configFile() string {
	return filepath.Join(m.state.Settings.Project, ".local", "publish.json")
}
func (m *Manager) ensureConfig() error {
	if _, e := os.Stat(m.configFile()); e == nil {
		return nil
	}
	b, e := os.ReadFile(filepath.Join(m.state.Settings.Project, "scripts", "publish.example.json"))
	if e != nil {
		return e
	}
	return writePrivate(m.configFile(), b)
}
func (m *Manager) saveSettingsLocked() error {
	if err := m.ensureConfig(); err != nil {
		return err
	}
	var cfg map[string]any
	if err := readJSON(m.configFile(), &cfg); err != nil {
		return err
	}
	u, ok := cfg["updater"].(map[string]any)
	if !ok {
		u = map[string]any{}
	}
	u["base_url"] = m.state.Settings.CloudURL
	u["enabled"] = m.state.Settings.CloudEnabled
	u["app_id"] = "bbdown-pro"
	u["publish"] = true
	cfg["updater"] = u
	if err := writeJSON(m.configFile(), cfg); err != nil {
		return err
	}
	return writeJSON(m.prefs, m.state.Settings)
}
func (m *Manager) Configure(s Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Busy {
		return errors.New("任务运行中，请先停止再修改设置")
	}
	abs, e := filepath.Abs(s.Project)
	if e != nil {
		return e
	}
	if e = validProject(abs); e != nil {
		return e
	}
	s.Project = abs
	s.CloudURL, e = cloudURL(s.CloudURL)
	if e != nil {
		return e
	}
	changed := s.Project != m.state.Settings.Project
	s.LastVersion = m.state.Settings.LastVersion
	if changed {
		s.LastVersion = ""
		m.state.Job = nil
		m.state.History = nil
	}
	m.state.Settings = s
	return m.saveSettingsLocked()
}
func (m *Manager) GetState() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(m.state)
	var copy State
	_ = json.Unmarshal(b, &copy)
	return copy
}
func toolEnv() []string {
	home, _ := os.UserHomeDir()
	p := os.Getenv("PATH") + string(os.PathListSeparator) + strings.Join([]string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", filepath.Join(home, ".local/bin"), filepath.Join(home, "Tools/go/bin")}, string(os.PathListSeparator))
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "PATH=") && !strings.HasPrefix(v, "BBDOWN_UPDATE_TOKEN=") {
			env = append(env, v)
		}
	}
	return append(env, "PATH="+p, "PYTHONUNBUFFERED=1", "UV_NO_PROGRESS=1")
}
func tool(name string) string {
	for _, v := range toolEnv() {
		if strings.HasPrefix(v, "PATH=") {
			for _, dir := range filepath.SplitList(strings.TrimPrefix(v, "PATH=")) {
				f := filepath.Join(dir, name)
				if s, e := os.Stat(f); e == nil && !s.IsDir() && s.Mode()&0111 != 0 {
					return f
				}
			}
		}
	}
	return ""
}
func probe(project, name string, args ...string) (string, error) {
	binary := tool(name)
	if binary == "" {
		return "", fmt.Errorf("未找到 %s", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = project
	cmd.Env = toolEnv()
	b, e := cmd.Output()
	if e != nil {
		return "", fmt.Errorf("%s 检查未通过", name)
	}
	return strings.TrimSpace(string(b)), nil
}
func (m *Manager) Refresh() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Busy {
		return m.copyLocked()
	}
	s := m.state.Settings
	m.state.Checks = nil
	m.state.Notice = ""
	add := func(id, name string, ok bool, msg string) {
		m.state.Checks = append(m.state.Checks, Check{id, name, ok, msg})
	}
	if e := validProject(s.Project); e != nil {
		add("project", "项目目录", false, e.Error())
		return m.copyLocked()
	}
	var info struct {
		Info struct {
			Version string `json:"productVersion"`
		} `json:"info"`
	}
	err := readJSON(filepath.Join(s.Project, "wails.json"), &info)
	m.state.Version = info.Info.Version
	add("project", "项目版本", err == nil && versionPattern.MatchString(m.state.Version), "v"+m.state.Version)
	commit, err := probe(s.Project, "git", "rev-parse", "HEAD")
	m.state.Commit = commit
	add("git", "Git 仓库", err == nil, "发布会固定到当前提交")
	dirty, err := probe(s.Project, "git", "status", "--porcelain")
	add("clean", "改动已提交", err == nil && dirty == "", "未提交改动需先提交；继续旧任务不受影响")
	for _, name := range []string{"uv", "gh"} {
		add(name, name+" 已安装", tool(name) != "", "在本机运行发布脚本")
	}
	_, err = probe(s.Project, "gh", "auth", "status")
	add("github", "GitHub 已登录", err == nil, "用于正式构建和下载安装包")
	_, err = probe(s.Project, "git", "remote", "get-url", "gitcode")
	add("gitcode-remote", "GitCode 远程仓库", err == nil, "用于同步固定提交和标签")
	chrome := false
	for _, f := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", tool("google-chrome"), tool("chromium")} {
		if f != "" {
			if _, e := os.Stat(f); e == nil {
				chrome = true
			}
		}
	}
	add("chrome", "Google Chrome", chrome, "用于 GitCode / 蓝奏云上传和独立下载校验")
	var cfg map[string]any
	_ = readJSON(m.configFile(), &cfg)
	profile := filepath.Join(s.Project, ".local/publish/lanzou-profile")
	if l, ok := cfg["lanzou"].(map[string]any); ok {
		if p, ok := l["profile"].(string); ok && p != "" {
			if filepath.IsAbs(p) {
				profile = p
			} else {
				profile = filepath.Join(s.Project, p)
			}
		}
	}
	for _, c := range []struct{ id, name, marker string }{{"gitcode-login", "GitCode 网页登录", "gitcode-login-ready"}, {"lanzou-login", "蓝奏云登录", "login-ready"}} {
		_, e := os.Stat(filepath.Join(profile, c.marker))
		ok := e == nil
		if c.id == "gitcode-login" && os.Getenv("GITCODE_TOKEN") != "" {
			ok = true
		}
		add(c.id, c.name, ok, "首次登录保存在独立 Chrome 目录；失效后可重新登录")
	}
	token, e := m.store.Get(s.CloudURL)
	m.state.CloudLoggedIn = e == nil && tokenValid(token)
	if s.CloudEnabled {
		add("cloud", "云端后台已登录", m.state.CloudLoggedIn, "用于登记下载源、试下载和正式发布")
	}
	m.loadHistoryLocked()
	return m.copyLocked()
}
func (m *Manager) copyLocked() State {
	b, _ := json.Marshal(m.state)
	var s State
	_ = json.Unmarshal(b, &s)
	return s
}
func (m *Manager) loadHistoryLocked() {
	m.state.History = []Job{}
	names, _ := filepath.Glob(filepath.Join(m.state.Settings.Project, ".local/publish/v*/publisher-job.json"))
	for _, f := range names {
		var j Job
		if readJSON(f, &j) == nil && versionPattern.MatchString(j.Version) && j.Project == m.state.Settings.Project {
			j.Logs = nil
			m.state.History = append(m.state.History, j)
		}
	}
	sort.Slice(m.state.History, func(i, j int) bool { return m.state.History[i].Updated > m.state.History[j].Updated })
}
func (m *Manager) Select(version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Busy {
		return errors.New("任务运行中")
	}
	if !versionPattern.MatchString(version) {
		return errors.New("版本号无效")
	}
	var j Job
	if err := readJSON(jobFile(m.state.Settings.Project, version), &j); err != nil {
		return errors.New("未找到任务记录")
	}
	if j.Project != m.state.Settings.Project {
		return errors.New("记录不属于当前项目")
	}
	if j.Status == "running" {
		j.Status = "interrupted"
	}
	m.state.Job = &j
	m.state.Settings.LastVersion = version
	return writeJSON(m.prefs, m.state.Settings)
}
func (m *Manager) LoginCloud(base, user, password string) error {
	base, e := cloudURL(base)
	if e != nil {
		return e
	}
	m.mu.Lock()
	if m.state.Busy {
		m.mu.Unlock()
		return errors.New("任务运行中，请稍后登录")
	}
	m.state.Busy = true
	m.state.Activity = "cloud-login"
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.state.Busy = false; m.state.Activity = ""; m.mu.Unlock() }()
	token, e := login(m.client, base, user, password)
	if e != nil {
		return e
	}
	if !tokenValid(token) {
		return errors.New("后台未返回有效的登录凭据")
	}
	if e = m.store.Set(base, token); e != nil {
		return e
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Settings.CloudURL = base
	m.state.Settings.CloudEnabled = true
	m.state.CloudLoggedIn = true
	if e = m.saveSettingsLocked(); e != nil {
		return e
	}
	m.state.Notice = "云端已登录，后续发布将自动登记并发布更新"
	return nil
}
func (m *Manager) LogoutCloud() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Busy {
		return errors.New("任务运行中")
	}
	if e := m.store.Delete(m.state.Settings.CloudURL); e != nil {
		return e
	}
	m.state.CloudLoggedIn = false
	return nil
}
func (m *Manager) Start(notes string, resume bool) error {
	m.Refresh()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Busy {
		return errors.New("已有任务在运行")
	}
	for _, c := range m.state.Checks {
		if resume && m.state.Job != nil && ((c.ID == "gitcode-login" && completed(m.state.Job, "gitcode")) || (c.ID == "lanzou-login" && completed(m.state.Job, "lanzou"))) {
			continue
		}
		if !c.OK && !(resume && (c.ID == "clean" || c.ID == "project")) {
			return fmt.Errorf("发布前检查未通过：%s", c.Name)
		}
	}
	if len(m.state.Checks) == 0 {
		return errors.New("请先选择项目并完成检查")
	}
	if len(notes) > 50000 {
		return errors.New("发布说明过长")
	}
	s := m.state.Settings
	j := m.state.Job
	if resume {
		if j == nil {
			return errors.New("没有可继续的发布任务")
		}
		if j.CloudEnabled && (j.CloudURL != s.CloudURL || !s.CloudEnabled) {
			return errors.New("请保持该任务原来的云端地址和联动设置")
		}
		if !j.CloudEnabled && s.CloudEnabled {
			j.CloudEnabled = true
			j.CloudURL = s.CloudURL
			for i := range j.Steps {
				if j.Steps[i].ID == "updater" {
					j.Steps[i].Status = "pending"
				}
			}
		}
		tag, e := probe(s.Project, "git", "rev-parse", "v"+j.Version+"^{commit}")
		if e == nil && tag != j.Commit {
			return errors.New("版本标签已改变，不能继续旧任务")
		}
		if e != nil && m.state.Commit != j.Commit {
			return errors.New("标签尚未创建且代码提交已变化，请重新开始发布")
		}
	} else {
		if j != nil && j.Status != "complete" && j.Version == m.state.Version {
			return errors.New("已有同版本任务，请点击继续发布")
		}
		if tag, e := probe(s.Project, "git", "tag", "--list", "v"+m.state.Version); e != nil || tag != "" {
			return errors.New("该版本标签已存在，请继续已有任务或更新版本号")
		}
		j = &Job{Version: m.state.Version, Commit: m.state.Commit, Project: s.Project, Notes: strings.TrimSpace(notes), Started: stamp(), Steps: newSteps(s.CloudEnabled), CloudURL: s.CloudURL, CloudEnabled: s.CloudEnabled}
		m.state.Job = j
	}
	j.Status = "running"
	j.Error = ""
	m.state.Settings.LastVersion = j.Version
	m.setStepLocked(Event{Step: "preflight", Status: "complete", Message: "检查通过，已固定版本与代码提交"})
	if e := writeJSON(m.prefs, m.state.Settings); e != nil {
		return e
	}
	if e := m.persistLocked(); e != nil {
		return e
	}
	args := []string{"run", filepath.Join(s.Project, "scripts/publish_release.py"), j.Version, "--events", "--expected-commit", j.Commit}
	snapshot := filepath.Join(s.Project, ".local/publish/v"+j.Version, "publisher-config.json")
	var config map[string]any
	if !resume || readJSON(snapshot, &config) != nil {
		if err := readJSON(m.configFile(), &config); err != nil {
			return err
		}
	}
	config["updater"] = map[string]any{"enabled": j.CloudEnabled, "base_url": j.CloudURL, "app_id": "bbdown-pro", "publish": true}
	if err := writeJSON(snapshot, config); err != nil {
		return err
	}
	args = append(args, "--config", snapshot)
	if j.Notes != "" {
		name := filepath.Join(s.Project, ".local/publish/v"+j.Version, "notes.md")
		if e := writePrivate(name, []byte(j.Notes)); e != nil {
			return e
		}
		args = append(args, "--notes-file", name)
	}
	if _, e := probe(s.Project, "git", "rev-parse", "v"+j.Version+"^{commit}"); e == nil {
		args = append(args, "--resume")
	}
	if s.VisibleBrowser {
		args = append(args, "--visible")
	}
	token := ""
	if j.CloudEnabled {
		var e error
		token, e = m.store.Get(j.CloudURL)
		if e != nil || !tokenValid(token) {
			j.Status = "failed"
			_ = m.persistLocked()
			return errors.New("云端登录已过期，请重新登录")
		}
	}
	if err := m.launchLocked("release", s.Project, "uv", args, token); err != nil {
		j.Status = "failed"
		j.Error = err.Error()
		_ = m.persistLocked()
		return err
	}
	return nil
}
func (m *Manager) LoginBrowser(channel string) error {
	if channel != "lanzou" && channel != "gitcode" && channel != "github" {
		return errors.New("未知登录渠道")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Busy {
		return errors.New("已有任务在运行，请先停止或等待完成")
	}
	if e := validProject(m.state.Settings.Project); e != nil {
		return e
	}
	if channel == "github" {
		return m.launchLocked("github-login", m.state.Settings.Project, "gh", []string{"auth", "login", "--hostname", "github.com", "--git-protocol", "ssh", "--skip-ssh-key", "--web", "--clipboard"}, "")
	}
	return m.launchLocked(channel+"-login", m.state.Settings.Project, "uv", []string{"run", filepath.Join(m.state.Settings.Project, "scripts/publish_release.py"), "--login-" + channel}, "")
}
func (m *Manager) persistLocked() error {
	if m.state.Job == nil {
		return nil
	}
	m.state.Job.Updated = stamp()
	return writeJSON(jobFile(m.state.Job.Project, m.state.Job.Version), m.state.Job)
}
func (m *Manager) setStepLocked(e Event) {
	if m.state.Job == nil {
		return
	}
	for i := range m.state.Job.Steps {
		step := &m.state.Job.Steps[i]
		if step.ID == e.Step {
			if e.Status != "pending" && e.Status != "running" && e.Status != "complete" && e.Status != "failed" && e.Status != "skipped" {
				return
			}
			step.Status = e.Status
			step.Message = m.redact(e.Message)
			step.Updated = stamp()
			if u, err := url.Parse(e.Link); err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" {
				step.Link = e.Link
			}
			return
		}
	}
}
func (m *Manager) redact(s string) string {
	if m.secret != "" {
		s = strings.ReplaceAll(s, m.secret, "[已隐藏]")
	}
	s = jwtPattern.ReplaceAllString(s, "[已隐藏]")
	s = querySecret.ReplaceAllString(s, "$1=[已隐藏]")
	if len(s) > 2000 {
		s = s[:2000] + "…"
	}
	return s
}
func (m *Manager) consume(line string, release bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	step := ""
	message := line
	if strings.HasPrefix(line, "BBDOWN_EVENT ") {
		var e Event
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "BBDOWN_EVENT ")), &e) != nil || e.Protocol != 1 {
			return
		}
		if release {
			m.setStepLocked(e)
		}
		step = e.Step
		message = e.Message
	}
	message = m.redact(strings.TrimSpace(message))
	if message == "" {
		return
	}
	if !release {
		m.state.ActivityLogs = append(m.state.ActivityLogs, Entry{stamp(), step, message})
		if len(m.state.ActivityLogs) > 100 {
			m.state.ActivityLogs = m.state.ActivityLogs[len(m.state.ActivityLogs)-100:]
		}
	} else if m.state.Job != nil {
		logs := m.state.Job.Logs
		if len(logs) == 0 || logs[len(logs)-1].Message != message {
			logs = append(logs, Entry{stamp(), step, message})
		}
		if len(logs) > 500 {
			logs = logs[len(logs)-500:]
		}
		m.state.Job.Logs = logs
		if e := m.persistLocked(); e != nil {
			m.state.Notice = "无法保存任务进度，发布已停止；请检查磁盘空间和权限"
			if m.cancel != nil {
				m.cancel()
			}
			return
		}
	}
	m.state.Notice = message
}
func (m *Manager) launchLocked(activity, project, name string, args []string, token string) error {
	binary := tool(name)
	if binary == "" {
		return fmt.Errorf("未找到 %s，请先安装", name)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = project
	cmd.Env = toolEnv()
	if token != "" {
		cmd.Env = append(cmd.Env, "BBDOWN_UPDATE_TOKEN="+token)
	}
	processDone := setupProcess(cmd)
	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer
	if e := cmd.Start(); e != nil {
		processDone()
		cancel()
		_ = reader.Close()
		_ = writer.Close()
		return errors.New("无法启动发布任务")
	}
	m.secret = token
	m.cancel = cancel
	m.state.Busy = true
	m.state.Activity = activity
	if activity != "release" {
		m.state.ActivityLogs = nil
	}
	m.state.Notice = "任务已启动"
	go func() {
		wait := make(chan error, 1)
		go func() { e := cmd.Wait(); processDone(); _ = writer.Close(); wait <- e }()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			m.consume(scanner.Text(), activity == "release")
		}
		if scanner.Err() != nil {
			cancel()
		}
		_ = reader.Close()
		err := <-wait
		m.mu.Lock()
		defer m.mu.Unlock()
		m.state.Busy = false
		m.state.Activity = ""
		m.cancel = nil
		m.secret = ""
		if activity == "release" && m.state.Job != nil {
			j := m.state.Job
			if ctx.Err() != nil {
				j.Status = "stopped"
				j.Error = "本机任务已停止，已完成步骤保留。GitHub 已提交的构建会继续运行。"
			} else if err != nil {
				j.Status = "failed"
				j.Error = m.state.Notice
			} else if !allCompleted(j) {
				j.Status = "failed"
				j.Error = "发布程序结束，但部分步骤没有确认完成；请继续任务检查"
			} else {
				j.Status = "complete"
				j.Error = ""
				m.state.Notice = "发布完成，所有启用的渠道已通过校验"
			}
			for i := range j.Steps {
				if j.Steps[i].Status == "running" {
					j.Steps[i].Status = "failed"
					j.Steps[i].Message = j.Error
				}
			}
			if e := m.persistLocked(); e != nil {
				j.Status = "failed"
				j.Error = "任务记录保存失败，请检查磁盘后重试"
			}
			m.loadHistoryLocked()
		} else if ctx.Err() != nil {
			m.state.Notice = "登录已取消"
		} else if err != nil {
			m.state.Notice = "登录未完成，请重试；详细信息已记录"
		} else {
			m.state.Notice = "登录完成，可以重新检查发布条件"
		}
		cancel()
	}()
	return nil
}
func completed(j *Job, id string) bool {
	for _, s := range j.Steps {
		if s.ID == id {
			return s.Status == "complete"
		}
	}
	return false
}
func allCompleted(j *Job) bool {
	for _, s := range j.Steps {
		if s.Status != "complete" && s.Status != "skipped" {
			return false
		}
	}
	return true
}
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.state.Notice = "正在停止本机任务并保存进度……"
	}
}
func (m *Manager) Link(target string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	project := m.state.Settings.Project
	var cfg struct {
		GitHub  string `json:"github_repo"`
		GitCode string `json:"gitcode_repo"`
		Lanzou  struct {
			URL string `json:"folder_share_url"`
		} `json:"lanzou"`
	}
	if e := readJSON(m.configFile(), &cfg); e != nil {
		return "", e
	}
	version := m.state.Version
	if m.state.Job != nil {
		version = m.state.Job.Version
	}
	switch target {
	case "build":
		if m.state.Job != nil {
			for _, s := range m.state.Job.Steps {
				if s.ID == "build" && s.Link != "" {
					return s.Link, nil
				}
			}
		}
		return "https://github.com/" + cfg.GitHub + "/actions", nil
	case "github":
		return "https://github.com/" + cfg.GitHub + "/releases/tag/v" + version, nil
	case "gitcode":
		return "https://gitcode.com/" + cfg.GitCode + "/releases", nil
	case "lanzou":
		return cfg.Lanzou.URL, nil
	case "updater":
		return m.state.Settings.CloudURL, nil
	case "assets":
		return filepath.Join(project, ".local/publish/v"+version), nil
	case "project":
		return project, nil
	case "screenshot":
		return filepath.Join(project, ".local/publish/lanzou-last-error.png"), nil
	}
	return "", errors.New("未知目标")
}
