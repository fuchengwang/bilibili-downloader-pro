package bilibili

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"bilibili_downloader/pkg/utils"
)

// QRCodeInfo 二维码信息
type QRCodeInfo struct {
	URL       string `json:"url"`       // 二维码内容 URL (前端可直接渲染)
	QRCodeKey string `json:"qrcodeKey"` // 轮询密钥
}

// QRStatus 二维码状态返回
type QRStatus struct {
	Code      int    `json:"code"`      // 0: 成功, 86101: 未扫码, 86090: 已扫未确认, 86038: 已过期
	Message   string `json:"message"`   // 状态文字描述
	IsSuccess bool   `json:"isSuccess"` // 是否登录成功
	IsExpired bool   `json:"isExpired"` // 是否已过期
}

// UserInfo 用户个人账号信息
type UserInfo struct {
	IsLogin     bool   `json:"isLogin"`
	Mid         int64  `json:"mid"`
	Uname       string `json:"uname"`
	Face        string `json:"face"`
	Level       int    `json:"level"`
	VipType     int    `json:"vipType"`     // 0: 无, 1: 月度大会员, 2: 年度及以上大会员
	VipStatus   int    `json:"vipStatus"`   // 1: 有效, 0: 无效
	VipLabel    string `json:"vipLabel"`    // 大会员标签文字
	VipDueDate  int64  `json:"vipDueDate"`  // 到期时间戳
	VipDueStr   string `json:"vipDueStr"`   // 到期时间文本
	Money       float64`json:"money"`       // 硬币数
}

type qrGenerateResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		URL       string `json:"url"`
		QRCodeKey string `json:"qrcode_key"`
	} `json:"data"`
}

type qrPollResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		URL          string `json:"url"`
		RefreshToken string `json:"refresh_token"`
		Code         int    `json:"code"`
		Message      string `json:"message"`
	} `json:"data"`
}

type navUserInfoResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		IsLogin bool   `json:"isLogin"`
		Mid     int64  `json:"mid"`
		Uname   string `json:"uname"`
		Face    string `json:"face"`
		LevelInfo struct {
			CurrentLevel int `json:"current_level"`
		} `json:"level_info"`
		VipType   int `json:"vipType"`
		VipStatus int `json:"vipStatus"`
		VipDueDate int64 `json:"vipDueDate"`
		VipLabel  struct {
			Text string `json:"text"`
		} `json:"vip_label"`
		Money float64 `json:"money"`
	} `json:"data"`
}

// GenerateQRCode 获取手机扫码登录二维码及 Key
func (c *Client) GenerateQRCode(ctx context.Context) (*QRCodeInfo, error) {
	apiURL := "https://passport.bilibili.com/x/passport-login/web/qrcode/generate"
	var resp qrGenerateResp
	if err := c.GetJSON(ctx, apiURL, &resp); err != nil {
		return nil, fmt.Errorf("获取二维码失败: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("二维码接口错误: %s", resp.Message)
	}
	return &QRCodeInfo{
		URL:       resp.Data.URL,
		QRCodeKey: resp.Data.QRCodeKey,
	}, nil
}

// PollQRCode 轮询二维码扫码状态
func (c *Client) PollQRCode(ctx context.Context, qrcodeKey string) (*QRStatus, error) {
	apiURL := fmt.Sprintf("https://passport.bilibili.com/x/passport-login/web/qrcode/poll?qrcode_key=%s", url.QueryEscape(qrcodeKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", BrowserUA)
	req.Header.Set("Referer", Referer)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var pResp qrPollResp
	if err := json.Unmarshal(body, &pResp); err != nil {
		return nil, fmt.Errorf("解析轮询响应失败: %w", err)
	}

	code := pResp.Data.Code
	status := &QRStatus{
		Code:    code,
		Message: pResp.Data.Message,
	}

	switch code {
	case 0:
		// 登录成功，从 Set-Cookie 头提取所有 cookie
		cookies := make(map[string]string)
		var sessData, biliJCT, buvid3, dedeUID string
		for _, cookie := range resp.Cookies() {
			cookies[cookie.Name] = cookie.Value
			switch cookie.Name {
			case "SESSDATA":
				sessData = cookie.Value
			case "bili_jct":
				biliJCT = cookie.Value
			case "buvid3":
				buvid3 = cookie.Value
			case "DedeUserID":
				dedeUID = cookie.Value
			}
		}

		if sessData != "" {
			cookieData := &CookieData{
				SessData: sessData,
				BiliJCT:  biliJCT,
				Buvid3:   buvid3,
				DedeUID:  dedeUID,
				Cookies:  cookies,
			}
			_ = c.SetCookies(cookieData)
			status.IsSuccess = true
			status.Message = "登录成功！"
		}
	case 86101:
		status.Message = "等待手机 App 扫码..."
	case 86090:
		status.Message = "扫码成功，请在手机上确认登录"
	case 86038:
		status.IsExpired = true
		status.Message = "二维码已失效，请点击刷新"
	default:
		if status.Message == "" {
			status.Message = fmt.Sprintf("状态码: %d", code)
		}
	}

	return status, nil
}

// OpenBrowserLogin 打开系统浏览器进入 B 站登录页面 (macOS 优先调用 Chrome / Edge 以便自动同步)
func (c *Client) OpenBrowserLogin() error {
	loginURL := "https://passport.bilibili.com/login"
	if runtime.GOOS == "darwin" {
		// 如果安装了 Chrome，优先用 Chrome 打开
		if _, err := os.Stat("/Applications/Google Chrome.app"); err == nil {
			if err := exec.Command("open", "-a", "Google Chrome", loginURL).Run(); err == nil {
				return nil
			}
		}
		// 如果安装了 Edge，用 Edge 打开
		if _, err := os.Stat("/Applications/Microsoft Edge.app"); err == nil {
			if err := exec.Command("open", "-a", "Microsoft Edge", loginURL).Run(); err == nil {
				return nil
			}
		}
		// 如果安装了 Brave
		if _, err := os.Stat("/Applications/Brave Browser.app"); err == nil {
			if err := exec.Command("open", "-a", "Brave Browser", loginURL).Run(); err == nil {
				return nil
			}
		}
	}
	return utils.OpenFile(loginURL)
}

// GetUserInfo 获取当前登录账号的详细资料
func (c *Client) GetUserInfo(ctx context.Context) (*UserInfo, error) {
	if !c.IsLoggedIn() {
		return &UserInfo{IsLogin: false}, nil
	}

	var resp navUserInfoResp
	apiURL := "https://api.bilibili.com/x/web-interface/nav"
	if err := c.GetJSON(ctx, apiURL, &resp); err != nil {
		return nil, fmt.Errorf("获取用户信息失败: %w", err)
	}

	if resp.Code != 0 || !resp.Data.IsLogin {
		return &UserInfo{IsLogin: false}, nil
	}

	d := resp.Data
	vipLabel := "普通用户"
	if d.VipStatus == 1 {
		if d.VipType == 2 {
			vipLabel = "年度大会员"
		} else if d.VipType == 1 {
			vipLabel = "月度大会员"
		}
	}

	dueStr := ""
	if d.VipDueDate > 0 {
		dueTime := time.UnixMilli(d.VipDueDate)
		dueStr = dueTime.Format("2006-01-02")
	}

	return &UserInfo{
		IsLogin:     true,
		Mid:         d.Mid,
		Uname:       d.Uname,
		Face:        d.Face,
		Level:       d.LevelInfo.CurrentLevel,
		VipType:     d.VipType,
		VipStatus:   d.VipStatus,
		VipLabel:    vipLabel,
		VipDueDate:  d.VipDueDate,
		VipDueStr:   dueStr,
		Money:       d.Money,
	}, nil
}

// ParseAndSaveRawCookie 从用户粘贴的 Cookie 字符串中提取关键字段并保存
func (c *Client) ParseAndSaveRawCookie(cookieStr string) error {
	cookieStr = strings.TrimSpace(cookieStr)
	if cookieStr == "" {
		return fmt.Errorf("输入的 Cookie 不能为空")
	}

	cookieMap := make(map[string]string)
	pairs := strings.Split(cookieStr, ";")
	for _, p := range pairs {
		p = strings.TrimSpace(p)
		if idx := strings.IndexByte(p, '='); idx > 0 {
			k := strings.TrimSpace(p[:idx])
			v := strings.TrimSpace(p[idx+1:])
			cookieMap[k] = v
		}
	}

	sessData := cookieMap["SESSDATA"]
	biliJCT := cookieMap["bili_jct"]
	buvid3 := cookieMap["buvid3"]
	dedeUID := cookieMap["DedeUserID"]

	if sessData == "" {
		return fmt.Errorf("Cookie 中未找到必要的 SESSDATA 字段")
	}

	cookieData := &CookieData{
		SessData: sessData,
		BiliJCT:  biliJCT,
		Buvid3:   buvid3,
		DedeUID:  dedeUID,
		Cookies:  cookieMap,
	}

	return c.SetCookies(cookieData)
}
