package publisher

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type TokenStore interface {
	Get(string) (string, error)
	Set(string, string) error
	Delete(string) error
}
type Keychain struct{}

func account(base string) string { h := sha256.Sum256([]byte(base)); return hex.EncodeToString(h[:16]) }
func (Keychain) Get(base string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("此版本的云端登录使用 macOS 钥匙串，请在 Mac 上运行发布器")
	}
	b, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", "com.bbdown.publisher.cloud", "-a", account(base), "-w").Output()
	if err != nil {
		return "", errors.New("请登录云端更新后台")
	}
	return strings.TrimSpace(string(b)), nil
}
func (Keychain) Set(base, token string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("此版本使用 macOS 钥匙串保存云端登录")
	}
	// security's interactive input keeps the token out of process arguments.
	quote := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	cmd := exec.Command("/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader("add-generic-password -U -s com.bbdown.publisher.cloud -a " + account(base) + " -w " + quote(token) + "\n")
	if err := cmd.Run(); err != nil {
		return errors.New("无法保存到系统钥匙串")
	}
	stored, err := (Keychain{}).Get(base)
	if err != nil || stored != token {
		return errors.New("系统钥匙串未确认登录保存成功")
	}
	return nil
}
func (Keychain) Delete(base string) error {
	_ = exec.Command("/usr/bin/security", "delete-generic-password", "-s", "com.bbdown.publisher.cloud", "-a", account(base)).Run()
	return nil
}
func cloudURL(base string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("云端地址应为 HTTPS 站点根地址，例如 https://47.97.111.181:8090")
	}
	return strings.TrimRight(u.String(), "/"), nil
}
func login(client *http.Client, base, user, password string) (string, error) {
	if user == "" || password == "" {
		return "", errors.New("请输入后台用户名和密码")
	}
	body, _ := json.Marshal(map[string]string{"username": user, "password": password})
	request, err := http.NewRequest("POST", base+"/api/v1/admin/login", bytes.NewReader(body))
	if err != nil {
		return "", errors.New("登录地址无效")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", errors.New("无法连接更新后台，请检查地址、网络和 HTTPS 证书")
	}
	defer response.Body.Close()
	var result struct {
		Success bool `json:"success"`
		Data    struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil || response.StatusCode != 200 || !result.Success || result.Data.Token == "" {
		return "", errors.New("后台登录未成功，请检查用户名和密码")
	}
	return result.Data.Token, nil
}
func tokenValid(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	// Only expiry is inspected locally; the server verifies its signature.
	return tokenExpiry(parts[1]).After(time.Now().Add(2 * time.Minute))
}
