package license

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bilibili_downloader/pkg/machineid"
)

var (
	ErrLicenseExpired      = errors.New("license has expired")
	ErrLicenseRevoked      = errors.New("license has been revoked")
	ErrDeviceLimitExceeded = errors.New("max device limit reached for this license")
	ErrServerUnreachable   = errors.New("cannot connect to license server")
	ErrInvalidLicenseKey   = errors.New("invalid license key")
)

type Client struct {
	config      Config
	hwInfo      *machineid.HardwareInfo
	storage     *Storage
	httpClient  *http.Client
	verifyMu    sync.Mutex
	isVerifying bool
	operationMu sync.Mutex // Serialize network mutations; local checks remain nonblocking.
	generation  atomic.Uint64
	lastAttempt atomic.Int64
}

// New 创建激活码客户端实例
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.AppID) == "" {
		return nil, errors.New("app_id is required")
	}
	if strings.TrimSpace(cfg.ServerURL) == "" {
		return nil, errors.New("server_url is required")
	}

	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.OfflineGraceDays <= 0 {
		cfg.OfflineGraceDays = 30
	}
	// AutoVerifyInterval == 0 explicitly means disabled background verification.
	if cfg.AppName == "" {
		cfg.AppName = cfg.AppID
	}

	hw := machineid.GetHardwareInfo(cfg.AppID)
	storage, err := NewStorage(cfg.AppID, hw.DeviceID, cfg.StoragePath)
	if err != nil {
		return nil, fmt.Errorf("failed to init storage: %w", err)
	}

	return &Client{
		config:  cfg,
		hwInfo:  hw,
		storage: storage,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}, nil
}

// GetDeviceInfo 获取当前设备的硬件指纹和系统信息
func (c *Client) GetDeviceInfo() *machineid.HardwareInfo {
	return c.hwInfo
}

// GetConfig 获取当前配置
func (c *Client) GetConfig() Config {
	return c.config
}

// CheckStatus 检查当前应用激活状态（优先本地极速校验，支持自动离线宽限与在线联网对齐）
func (c *Client) CheckStatus(ctx context.Context, forceOnline bool) (*LicenseStatus, error) {
	token, err := c.storage.Load(c.hwInfo.DeviceID)
	if err != nil {
		if errors.Is(err, ErrNoLicenseFound) {
			return &LicenseStatus{
				IsActivated: false,
				AppID:       c.config.AppID,
				AppName:     c.config.AppName,
				DeviceID:    c.hwInfo.DeviceID,
				DeviceName:  c.hwInfo.Hostname,
				Message:     "应用尚未激活，请输入激活码",
			}, nil
		}
		if errors.Is(err, ErrLicenseTampered) {
			return &LicenseStatus{
				IsActivated: false,
				AppID:       c.config.AppID,
				AppName:     c.config.AppName,
				DeviceID:    c.hwInfo.DeviceID,
				DeviceName:  c.hwInfo.Hostname,
				Message:     "授权凭证损坏或设备指纹不匹配，请重新激活",
			}, err
		}
		if errors.Is(err, ErrTimeClockRollback) {
			return &LicenseStatus{
				IsActivated: false,
				AppID:       c.config.AppID,
				AppName:     c.config.AppName,
				DeviceID:    c.hwInfo.DeviceID,
				DeviceName:  c.hwInfo.Hostname,
				Message:     "检测到系统时间被异常回拨，授权已暂停",
			}, err
		}
		return nil, err
	}

	// 1. 本地到期校验
	now := time.Now().Unix()
	if !token.IsPermanent && token.ExpiresAt > 0 && token.ExpiresAt <= now {
		if forceOnline {
			return c.verifyOnline(ctx, token.LicenseKey)
		}
		return &LicenseStatus{
			IsActivated: false,
			LicenseKey:  token.LicenseKey,
			AppID:       c.config.AppID,
			AppName:     c.config.AppName,
			DeviceID:    c.hwInfo.DeviceID,
			DeviceName:  c.hwInfo.Hostname,
			DaysLeft:    0,
			Message:     "您的卡密授权已过期，请续费或更换激活码",
		}, ErrLicenseExpired
	}

	// 2. 本地验签 (若配置了 AppSecret)
	if c.config.AppSecret != "" {
		expectedSig := GenerateSignature(token.AppID, token.LicenseKey, token.DeviceID, token.ExpiresAt, c.config.AppSecret)
		if !hmac.Equal([]byte(token.Signature), []byte(expectedSig)) {
			return &LicenseStatus{
				IsActivated: false,
				AppID:       c.config.AppID,
				AppName:     c.config.AppName,
				DeviceID:    c.hwInfo.DeviceID,
				DeviceName:  c.hwInfo.Hostname,
				Message:     "授权凭证签名校验失败",
			}, ErrLicenseTampered
		}
	}

	// 构建本地状态结果
	var expireTime *time.Time
	daysLeft := 99999
	if !token.IsPermanent && token.ExpiresAt > 0 {
		t := time.Unix(token.ExpiresAt, 0)
		expireTime = &t
		if token.ExpiresAt > now {
			daysLeft = int((token.ExpiresAt - now + 86399) / 86400)
		} else {
			daysLeft = 0
		}
	}
	actTime := time.Unix(token.ActivatedAt, 0)
	lastVerifiedUnix := token.LastVerifiedAt
	if lastVerifiedUnix == 0 {
		lastVerifiedUnix = token.ActivatedAt
	}
	lastVerifiedTime := time.Unix(lastVerifiedUnix, 0)

	localStatus := &LicenseStatus{
		IsActivated:      true,
		LicenseKey:       token.LicenseKey,
		AppID:            c.config.AppID,
		AppName:          c.config.AppName,
		IsPermanent:      token.IsPermanent,
		ExpireAt:         expireTime,
		DaysLeft:         daysLeft,
		DeviceID:         token.DeviceID,
		DeviceName:       token.DeviceName,
		MaxDevices:       token.MaxDevices,
		ActivatedDevices: 1,
		ActivatedAt:      &actTime,
		LastVerifiedAt:   &lastVerifiedTime,
		IsOfflineValid:   true,
		Message:          "激活有效 (离线校验通过)",
	}

	// 3. 在线联机验证
	if forceOnline {
		onlineStatus, onlineErr := c.verifyOnline(ctx, token.LicenseKey)
		if onlineErr == nil {
			return onlineStatus, nil
		}
		// 如果是明确的服务端拒绝（吊销/过期/解绑等），返回错误并不再允许离线
		if errors.Is(onlineErr, errLicenseChanged) || errors.Is(onlineErr, ErrNoLicenseFound) {
			return c.CheckStatus(ctx, false)
		}
		if isLicenseRejected(onlineErr) {
			return &LicenseStatus{
				IsActivated: false,
				LicenseKey:  token.LicenseKey,
				AppID:       c.config.AppID,
				AppName:     c.config.AppName,
				DeviceID:    c.hwInfo.DeviceID,
				Message:     onlineErr.Error(),
			}, onlineErr
		}
		// 服务暂不可用时保留有效的本地授权。
		localStatus.Message = fmt.Sprintf("激活有效 (离线模式: %s)", onlineErr.Error())
	} else if c.config.AutoVerifyInterval > 0 && c.config.RefundProtectionDays > 0 {
		// 4. 退款保护期内发送异步静默心跳 (防阻塞、防重发、最多重试2次，网络失败绝不影响正常使用)
		protectionEndTime := actTime.Add(time.Duration(c.config.RefundProtectionDays) * 24 * time.Hour)
		if time.Now().Before(protectionEndTime) {
			if time.Since(lastVerifiedTime) > c.config.AutoVerifyInterval && time.Since(time.Unix(c.lastAttempt.Load(), 0)) > c.config.AutoVerifyInterval {
				c.triggerAsyncVerify(token)
			}
		}
	}

	return localStatus, nil
}

// triggerAsyncVerify 触发后台静默心跳核验：具备防并发重发保护、最多重试2次（间隔递增）。
// 失败时直接优雅退出并把下一次尝试推迟到下个周期，绝不卡顿、绝不弹错、绝不反复刷请求！
func (c *Client) triggerAsyncVerify(token *LicenseToken) {
	generation := c.generation.Load()
	c.verifyMu.Lock()
	if c.isVerifying {
		c.verifyMu.Unlock()
		return
	}
	c.isVerifying = true
	c.verifyMu.Unlock()

	go func(tok *LicenseToken) {
		defer func() {
			c.verifyMu.Lock()
			c.isVerifying = false
			c.verifyMu.Unlock()
		}()

		// 重试策略：首次请求立即发起，失败后等待 3 秒第 1 次重试，再失败等待 6 秒第 2 次重试
		delays := []time.Duration{0, 3 * time.Second, 6 * time.Second}

		for attempt, delay := range delays {
			if delay > 0 {
				time.Sleep(delay)
			}

			bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := c.verifyOnlineAt(bgCtx, tok.LicenseKey, generation)
			cancel()

			// 1. 成功核验，或者明确收到服务端拒绝（如已被拉黑/吊销/无效），直接结束流程
			if err == nil {
				return
			}
			if isLicenseRejected(err) || errors.Is(err, errLicenseChanged) {
				return
			}

			// 2. 如果连续 3 次尝试（含2次重试）因网络不可达/服务器维护而失败：
			// 仅记录进程内重试冷却，不能把失败伪装成验证成功或重新写入旧凭证。
			if attempt == len(delays)-1 {
				c.lastAttempt.Store(time.Now().Unix())
				return
			}
		}
	}(token)
}

// Activate 向服务端发起激活请求
func (c *Client) Activate(ctx context.Context, licenseKey string) (*LicenseStatus, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	c.generation.Add(1)
	c.lastAttempt.Store(0)
	key := strings.TrimSpace(licenseKey)
	if key == "" {
		return nil, errors.New("激活码不能为空")
	}

	reqBody := ActivateRequest{
		AppID:         c.config.AppID,
		LicenseKey:    key,
		DeviceID:      c.hwInfo.DeviceID,
		DeviceName:    c.hwInfo.Hostname,
		OS:            c.hwInfo.OS,
		Arch:          c.hwInfo.Arch,
		MAC:           c.hwInfo.MACAddress,
		ClientVersion: c.config.ClientVersion,
		Timestamp:     time.Now().Unix(),
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	apiURL := fmt.Sprintf("%s/api/v1/license/activate", c.config.ServerURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerUnreachable, err)
	}
	defer resp.Body.Close()

	var apiResp ApiResponse[LicenseToken]
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("解析服务器返回失败: %w", err)
	}

	if !apiResp.Success {
		return nil, errors.New(apiResp.Message)
	}

	token := apiResp.Data
	nowUnix := time.Now().Unix()
	token.LastSeenTime = nowUnix
	token.LastVerifiedAt = nowUnix

	// 保存加密凭证至本地
	if err := c.storage.Save(&token); err != nil {
		return nil, fmt.Errorf("写入本地授权文件失败: %w", err)
	}

	var expireTime *time.Time
	daysLeft := 99999
	if !token.IsPermanent && token.ExpiresAt > 0 {
		t := time.Unix(token.ExpiresAt, 0)
		expireTime = &t
		if token.ExpiresAt > nowUnix {
			daysLeft = int((token.ExpiresAt - nowUnix + 86399) / 86400)
		} else {
			daysLeft = 0
		}
	}
	actTime := time.Unix(token.ActivatedAt, 0)
	nowTime := time.Now()

	return &LicenseStatus{
		IsActivated:      true,
		LicenseKey:       token.LicenseKey,
		AppID:            c.config.AppID,
		AppName:          c.config.AppName,
		IsPermanent:      token.IsPermanent,
		ExpireAt:         expireTime,
		DaysLeft:         daysLeft,
		DeviceID:         token.DeviceID,
		DeviceName:       token.DeviceName,
		MaxDevices:       token.MaxDevices,
		ActivatedDevices: 1,
		ActivatedAt:      &actTime,
		LastVerifiedAt:   &nowTime,
		IsOfflineValid:   true,
		Message:          "激活成功！",
	}, nil
}

// Deactivate 解绑当前设备
func (c *Client) Deactivate(ctx context.Context) error {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	c.generation.Add(1)
	token, err := c.storage.Load(c.hwInfo.DeviceID)
	if errors.Is(err, ErrNoLicenseFound) {
		return nil
	}
	if err != nil {
		return err
	}
	body, err := json.Marshal(DeactivateRequest{AppID: c.config.AppID, LicenseKey: token.LicenseKey, DeviceID: c.hwInfo.DeviceID, Timestamp: time.Now().Unix()})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.ServerURL+"/api/v1/license/deactivate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("解绑请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("解绑请求失败: HTTP %d", resp.StatusCode)
	}
	var result ApiResponse[any]
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("解析解绑结果失败: %w", err)
	}
	if !result.Success {
		return fmt.Errorf("解绑失败: %s", result.Message)
	}
	return c.storage.Delete()
}

var errLicenseChanged = errors.New("license changed during verification")

// Existing servers use message text for business errors; keep the wire protocol unchanged.
func isLicenseRejected(err error) bool {
	if err == nil {
		return false
	}
	for _, text := range []string{"吊销", "过期", "超出有效期限", "解绑", "卡密不存在", "尚未激活", "应用不存在"} {
		if strings.Contains(err.Error(), text) {
			return true
		}
	}
	return errors.Is(err, ErrLicenseRevoked) || errors.Is(err, ErrLicenseExpired) || errors.Is(err, ErrInvalidLicenseKey)
}

// verifyOnline 联网验证
func (c *Client) verifyOnline(ctx context.Context, licenseKey string) (*LicenseStatus, error) {
	return c.verifyOnlineAt(ctx, licenseKey, c.generation.Load())
}

func (c *Client) verifyOnlineAt(ctx context.Context, licenseKey string, generation uint64) (*LicenseStatus, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	if generation != c.generation.Load() {
		return nil, errLicenseChanged
	}
	current, err := c.storage.Load(c.hwInfo.DeviceID)
	if err != nil {
		return nil, err
	}
	if current.LicenseKey != licenseKey {
		return nil, errLicenseChanged
	}

	reqBody := VerifyRequest{
		AppID:      c.config.AppID,
		LicenseKey: licenseKey,
		DeviceID:   c.hwInfo.DeviceID,
		Timestamp:  time.Now().Unix(),
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	apiURL := fmt.Sprintf("%s/api/v1/license/verify", c.config.ServerURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerUnreachable, err)
	}
	defer resp.Body.Close()

	var apiResp ApiResponse[LicenseToken]
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("解析服务器返回失败: %w", err)
	}

	if !apiResp.Success {
		// 服务端明确返回非法或已吊销 -> 清除本地缓存
		if isLicenseRejected(errors.New(apiResp.Message)) {
			_ = c.storage.Delete()
		}
		return nil, errors.New(apiResp.Message)
	}

	token := apiResp.Data
	nowUnix := time.Now().Unix()
	token.LastSeenTime = nowUnix
	token.LastVerifiedAt = nowUnix
	if err := c.storage.Save(&token); err != nil {
		return nil, fmt.Errorf("保存授权失败: %w", err)
	}

	var expireTime *time.Time
	daysLeft := 99999
	if !token.IsPermanent && token.ExpiresAt > 0 {
		t := time.Unix(token.ExpiresAt, 0)
		expireTime = &t
		if token.ExpiresAt > nowUnix {
			daysLeft = int((token.ExpiresAt - nowUnix + 86399) / 86400)
		} else {
			daysLeft = 0
		}
	}
	actTime := time.Unix(token.ActivatedAt, 0)
	nowTime := time.Now()

	return &LicenseStatus{
		IsActivated:      true,
		LicenseKey:       token.LicenseKey,
		AppID:            c.config.AppID,
		AppName:          c.config.AppName,
		IsPermanent:      token.IsPermanent,
		ExpireAt:         expireTime,
		DaysLeft:         daysLeft,
		DeviceID:         token.DeviceID,
		DeviceName:       token.DeviceName,
		MaxDevices:       token.MaxDevices,
		ActivatedDevices: 1,
		ActivatedAt:      &actTime,
		LastVerifiedAt:   &nowTime,
		IsOfflineValid:   true,
		Message:          "在线验证通过",
	}, nil
}

// GenerateSignature 计算防篡改签名
func GenerateSignature(appID, licenseKey, deviceID string, expiresAt int64, secret string) string {
	payload := fmt.Sprintf("%s|%s|%s|%d", appID, licenseKey, deviceID, expiresAt)
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(payload))
	return hex.EncodeToString(h.Sum(nil))
}
