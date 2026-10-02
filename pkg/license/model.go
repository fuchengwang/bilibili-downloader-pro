package license

import "time"

// LicenseStatus 表示当前客户端的授权状态
type LicenseStatus struct {
	IsActivated      bool       `json:"is_activated"`      // 是否已激活且有效
	LicenseKey       string     `json:"license_key"`       // 当前使用的卡密
	AppID            string     `json:"app_id"`            // 应用唯一标识
	AppName          string     `json:"app_name"`          // 应用名称
	IsPermanent      bool       `json:"is_permanent"`      // 是否永久授权
	ExpireAt         *time.Time `json:"expire_at"`         // 到期时间（永久授权时为 nil）
	DaysLeft         int        `json:"days_left"`         // 剩余有效天数（永久为 99999）
	DeviceID         string     `json:"device_id"`         // 本机设备指纹
	DeviceName       string     `json:"device_name"`       // 本机设备名称
	MaxDevices       int        `json:"max_devices"`       // 该卡密最大允许激活设备数
	ActivatedDevices int        `json:"activated_devices"` // 当前已激活的设备数
	ActivatedAt      *time.Time `json:"activated_at"`      // 本机激活时间
	LastVerifiedAt   *time.Time `json:"last_verified_at"`  // 上次验证时间
	IsOfflineValid   bool       `json:"is_offline_valid"`  // 本地离线校验通过
	Message          string     `json:"message"`           // 状态说明或提示
}

// LicenseToken 储存在本地并由服务端签名的核心凭证
type LicenseToken struct {
	AppID          string         `json:"app_id"`
	LicenseKey     string         `json:"license_key"`
	DeviceID       string         `json:"device_id"`
	DeviceName     string         `json:"device_name"`
	IsPermanent    bool           `json:"is_permanent"`
	ExpiresAt      int64          `json:"expires_at"`   // Unix 时间戳，0 代表永久
	ActivatedAt    int64          `json:"activated_at"` // Unix 时间戳
	MaxDevices     int            `json:"max_devices"`
	Signature      string         `json:"signature"`        // 服务端 HMAC-SHA256 签名
	LastSeenTime   int64          `json:"last_seen_time"`   // 本地运行时间戳（防篡改与防回拨）
	LastVerifiedAt int64          `json:"last_verified_at"` // 最近一次与服务端成功核对的时间戳
	Extra          map[string]any `json:"extra,omitempty"`  // 扩展字段 (预留供服务端未来业务灵活扩展)
}

// ActivateRequest 客户端发起激活的请求结构
type ActivateRequest struct {
	AppID         string         `json:"app_id"`
	LicenseKey    string         `json:"license_key"`
	DeviceID      string         `json:"device_id"`
	DeviceName    string         `json:"device_name"`
	OS            string         `json:"os"`
	Arch          string         `json:"arch"`
	MAC           string         `json:"mac"`
	ClientVersion string         `json:"client_version"`
	Timestamp     int64          `json:"timestamp"`
	Extra         map[string]any `json:"extra,omitempty"`
}

// VerifyRequest 客户端发起心跳/校验的请求结构
type VerifyRequest struct {
	AppID      string         `json:"app_id"`
	LicenseKey string         `json:"license_key"`
	DeviceID   string         `json:"device_id"`
	Timestamp  int64          `json:"timestamp"`
	Extra      map[string]any `json:"extra,omitempty"`
}

// DeactivateRequest 客户端发起解绑的请求结构
type DeactivateRequest struct {
	AppID      string         `json:"app_id"`
	LicenseKey string         `json:"license_key"`
	DeviceID   string         `json:"device_id"`
	Timestamp  int64          `json:"timestamp"`
	Extra      map[string]any `json:"extra,omitempty"`
}

// ApiResponse 通用接口返回封装
type ApiResponse[T any] struct {
	Code    int    `json:"code"`
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

// ClientConfig 客户端配置选项
type Config struct {
	ServerURL            string        `json:"server_url"`             // 授权服务端地址，如 http://127.0.0.1:8888
	AppID                string        `json:"app_id"`                 // 应用标识
	AppName              string        `json:"app_name"`               // 应用展示名称
	AppSecret            string        `json:"app_secret"`             // 应用密钥（用于本地离线验签）
	StoragePath          string        `json:"storage_path"`           // 自定义授权文件存储路径（为空则使用系统标准路径）
	ClientVersion        string        `json:"client_version"`         // 客户端版本
	Timeout              time.Duration `json:"timeout"`                // 网络请求超时
	OfflineGraceDays     int           `json:"offline_grace_days"`     // 允许离线运行的最大天数
	AutoVerifyInterval   time.Duration `json:"auto_verify_interval"`   // 低频静默联网核对周期 (默认 24 小时，0 代表不自动核对)
	RefundProtectionDays int           `json:"refund_protection_days"` // 退款保护期(天数)。激活后在此天数内会发送静默心跳，超过后彻底离线。0 代表不启用。
}
