package publisher

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const DefaultCloud = "https://47.97.111.181:8090"

type Settings struct {
	Project        string `json:"project"`
	CloudURL       string `json:"cloudURL"`
	CloudEnabled   bool   `json:"cloudEnabled"`
	VisibleBrowser bool   `json:"visibleBrowser"`
	LastVersion    string `json:"lastVersion"`
}
type Check struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}
type Step struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Link    string `json:"link"`
	Updated string `json:"updated"`
}
type Entry struct {
	Time    string `json:"time"`
	Step    string `json:"step"`
	Message string `json:"message"`
}
type Job struct {
	Version      string  `json:"version"`
	Commit       string  `json:"commit"`
	Project      string  `json:"project"`
	Notes        string  `json:"notes"`
	Status       string  `json:"status"`
	Error        string  `json:"error"`
	Started      string  `json:"started"`
	Updated      string  `json:"updated"`
	Steps        []Step  `json:"steps"`
	Logs         []Entry `json:"logs"`
	CloudURL     string  `json:"cloudURL"`
	CloudEnabled bool    `json:"cloudEnabled"`
}
type State struct {
	Settings      Settings `json:"settings"`
	Version       string   `json:"version"`
	Commit        string   `json:"commit"`
	Checks        []Check  `json:"checks"`
	Job           *Job     `json:"job"`
	History       []Job    `json:"history"`
	Busy          bool     `json:"busy"`
	Activity      string   `json:"activity"`
	Notice        string   `json:"notice"`
	CloudLoggedIn bool     `json:"cloudLoggedIn"`
	ActivityLogs  []Entry  `json:"activityLogs"`
}
type Event struct {
	Protocol int    `json:"protocol"`
	Step     string `json:"step"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Link     string `json:"link"`
}

func stamp() string { return time.Now().UTC().Format(time.RFC3339) }
func newSteps(cloud bool) []Step {
	steps := []Step{{ID: "preflight", Name: "发布前检查"}, {ID: "build", Name: "双平台构建 · 签名公证"},
		{ID: "assets", Name: "下载安装包 · 校验"}, {ID: "gitcode", Name: "GitCode 发布 · 下载校验"},
		{ID: "lanzou", Name: "蓝奏云上传 · 匿名校验"}, {ID: "updater", Name: "云端登记 · 试下载 · 发布"}}
	for i := range steps {
		steps[i].Status = "pending"
	}
	if !cloud {
		steps[5].Status = "skipped"
		steps[5].Message = "本次未启用云端联动"
	}
	return steps
}
func jobFile(project, version string) string {
	return filepath.Join(project, ".local", "publish", "v"+version, "publisher-job.json")
}
func readJSON(name string, dst any) error {
	b, e := os.ReadFile(name)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, dst)
}
func writeJSON(name string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(name, append(b, '\n'))
}
func writePrivate(name string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".publisher-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), name)
}
func validProject(project string) error {
	for _, name := range []string{"wails.json", "scripts/publish_release.py", "scripts/publish.example.json"} {
		info, err := os.Stat(filepath.Join(project, name))
		if err != nil || info.IsDir() {
			return errors.New("请选择 BBDown Pro 的项目目录")
		}
	}
	return nil
}
