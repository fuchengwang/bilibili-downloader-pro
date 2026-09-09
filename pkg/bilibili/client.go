package bilibili

import (
	"context"

	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bilibili_downloader/pkg/config"
)

const (
	BrowserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"
	Referer   = "https://www.bilibili.com/"
)

// CookieData 存储在本地的 B站 Cookie 凭证
type CookieData struct {
	SessData string            `json:"SESSDATA"`
	BiliJCT  string            `json:"bili_jct"`
	Buvid3   string            `json:"buvid3"`
	DedeUID  string            `json:"DedeUserID"`
	Cookies  map[string]string `json:"cookies"`
}

// Client 封装与 B 站所有交互的 HTTP 客户端
type Client struct {
	mu          sync.RWMutex
	httpClient  *http.Client
	cookieData  *CookieData
	wbiMixin    string
	wbiCached   time.Time
	clockOffset atomic.Int64 // 服务端与本地系统时钟差值 (秒)，彻底消除本地时间不准导致的 WBI 签名失效
}

var (
	defaultClient *Client
	clientOnce    sync.Once
)

// GetDefaultClient 获取全局单例 Bilibili API 客户端
func GetDefaultClient() *Client {
	clientOnce.Do(func() {
		tr := &http.Transport{
			Proxy:               http.ProxyFromEnvironment,

			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 20,
			IdleConnTimeout:     90 * time.Second,
		}
		c := &Client{
			httpClient: &http.Client{
				Transport: tr,
				Timeout:   30 * time.Second,
			},
			cookieData: &CookieData{
				Cookies: make(map[string]string),
			},
		}
		c.loadCookies()
		defaultClient = c
	})
	return defaultClient
}

// SetCookies 更新并持久化 Cookie
func (c *Client) SetCookies(data *CookieData) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cookieData = data
	// 刷新 wbi 缓存
	c.wbiMixin = ""

	cookiePath := config.GetManager().GetCookiesPath()
	bytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cookiePath, bytes, 0600)
}

// ClearCookies 清理 Cookie
func (c *Client) ClearCookies() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cookieData = &CookieData{Cookies: make(map[string]string)}
	c.wbiMixin = ""
	cookiePath := config.GetManager().GetCookiesPath()
	_ = os.Remove(cookiePath)
	return nil
}

// GetCookies 获取当前内存中的 CookieData 快照
func (c *Client) GetCookies() *CookieData {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cookieData == nil {
		return nil
	}
	cp := *c.cookieData
	return &cp
}

// GetCookieHeader 构造用于请求头的 Cookie 字符串
func (c *Client) GetCookieHeader() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cookieData == nil {
		return ""
	}
	var pairs []string
	if c.cookieData.SessData != "" {
		pairs = append(pairs, "SESSDATA="+c.cookieData.SessData)
	}
	if c.cookieData.BiliJCT != "" {
		pairs = append(pairs, "bili_jct="+c.cookieData.BiliJCT)
	}
	if c.cookieData.Buvid3 != "" {
		pairs = append(pairs, "buvid3="+c.cookieData.Buvid3)
	}
	if c.cookieData.DedeUID != "" {
		pairs = append(pairs, "DedeUserID="+c.cookieData.DedeUID)
	}
	for k, v := range c.cookieData.Cookies {
		if k != "SESSDATA" && k != "bili_jct" && k != "buvid3" && k != "DedeUserID" {
			pairs = append(pairs, fmt.Sprintf("%s=%s", k, v))
		}
	}
	return strings.Join(pairs, "; ")
}

// IsLoggedIn 检查当前是否已保存登录凭证
func (c *Client) IsLoggedIn() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cookieData != nil && c.cookieData.SessData != ""
}

func (c *Client) loadCookies() {
	cookiePath := config.GetManager().GetCookiesPath()
	data, err := os.ReadFile(cookiePath)
	if err != nil {
		return
	}
	var cd CookieData
	if err := json.Unmarshal(data, &cd); err == nil {
		if cd.Cookies == nil {
			cd.Cookies = make(map[string]string)
		}
		c.cookieData = &cd
	}
}

// GetBytes 发送带有标准 Bilibili 请求头的 GET 请求
func (c *Client) GetBytes(ctx context.Context, reqURL string, extraHeaders map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", BrowserUA)
	req.Header.Set("Referer", Referer)
	if cookieHeader := c.GetCookieHeader(); cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	c.UpdateClockOffset(resp.Header.Get("Date"))

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP error: %s", resp.Status)
	}

	return io.ReadAll(resp.Body)
}

// UpdateClockOffset 根据 HTTP 响应头中的 Date 字段动态校准本地与 B 站服务器时钟偏差
func (c *Client) UpdateClockOffset(serverDateStr string) {
	if serverDateStr == "" {
		return
	}
	if t, err := http.ParseTime(serverDateStr); err == nil {
		diff := t.Unix() - time.Now().Unix()
		c.clockOffset.Store(diff)
		return
	}
	if t, err := time.Parse(time.RFC1123, serverDateStr); err == nil {
		diff := t.Unix() - time.Now().Unix()
		c.clockOffset.Store(diff)
		return
	}
	if t, err := time.Parse(time.RFC1123Z, serverDateStr); err == nil {
		diff := t.Unix() - time.Now().Unix()
		c.clockOffset.Store(diff)
		return
	}
}

// GetClockOffset 获取当前服务器时钟与本地时钟差值 (秒)
func (c *Client) GetClockOffset() int64 {
	return c.clockOffset.Load()
}

// GetJSON 请求并将响应反序列化为目标结构体
func (c *Client) GetJSON(ctx context.Context, reqURL string, target any) error {
	body, err := c.GetBytes(ctx, reqURL, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("failed to parse JSON response: %w", err)
	}
	return nil
}

// PostForm 发送 POST 请求并返回数据
func (c *Client) PostForm(ctx context.Context, reqURL string, data url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", BrowserUA)
	req.Header.Set("Referer", Referer)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookieHeader := c.GetCookieHeader(); cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	c.UpdateClockOffset(resp.Header.Get("Date"))

	return io.ReadAll(resp.Body)
}

// FinalURL 跟随重定向，返回重定向后的真实最终 URL (主要用于展开 b23.tv 短链)
func (c *Client) FinalURL(ctx context.Context, reqURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", BrowserUA)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return resp.Request.URL.String(), nil
}
