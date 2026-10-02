package machineid

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
)

// HardwareInfo 包含机器的硬件与环境基本信息
type HardwareInfo struct {
	DeviceID   string `json:"device_id"`
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	MACAddress string `json:"mac_address"`
	RawUUID    string `json:"raw_uuid,omitempty"`
}

// GetID 基于系统唯一硬件信息和指定的 AppID 生成确定性唯一的设备指纹
// 返回形如: 8a7f1c2d-9b3e-4f5a-8c1d-2e3f4a5b6c7d
func GetID(appID string) (string, error) {
	rawID, err := getRawMachineID()
	if err != nil || strings.TrimSpace(rawID) == "" {
		rawID = getFallbackID()
	}

	rawID = strings.TrimSpace(rawID)
	if appID == "" {
		appID = "go-license-default-app"
	}

	// 使用 HMAC-SHA256 对原始硬件标识和 AppID 进行哈希，确保不同应用间的指纹隔离与防伪
	h := hmac.New(sha256.New, []byte(appID))
	h.Write([]byte(rawID))
	hash := hex.EncodeToString(h.Sum(nil))

	// 格式化为标准的 UUID 类似格式 8-4-4-4-12
	if len(hash) >= 32 {
		return fmt.Sprintf("%s-%s-%s-%s-%s",
			hash[0:8],
			hash[8:12],
			hash[12:16],
			hash[16:20],
			hash[20:32],
		), nil
	}

	return hash, nil
}

// GetHardwareInfo 获取本机系统与硬件环境信息
func GetHardwareInfo(appID string) *HardwareInfo {
	deviceID, _ := GetID(appID)
	hostname, _ := os.Hostname()
	mac := getFirstMACAddress()

	return &HardwareInfo{
		DeviceID:   deviceID,
		Hostname:   hostname,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		MACAddress: mac,
	}
}

// getFallbackID 当底层特定系统UUID无法读取时的可靠备用指纹生成逻辑
func getFallbackID() string {
	var parts []string

	// 1. Hostname
	hostname, err := os.Hostname()
	if err == nil && hostname != "" {
		parts = append(parts, "host:"+hostname)
	}

	// 2. MAC 地址集合
	mac := getFirstMACAddress()
	if mac != "" {
		parts = append(parts, "mac:"+mac)
	}

	// 3. 用户主目录与系统架构
	home, _ := os.UserHomeDir()
	parts = append(parts, "home:"+home, "arch:"+runtime.GOARCH, "os:"+runtime.GOOS)

	// SHA256 合并计算
	hasher := sha256.New()
	hasher.Write([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(hasher.Sum(nil))
}

// getFirstMACAddress 获取本机第一个非虚拟物理网卡的 MAC 地址
func getFirstMACAddress() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	for _, iface := range interfaces {
		// 忽略回环和下线网卡
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		mac := iface.HardwareAddr.String()
		if mac != "" {
			return mac
		}
	}
	return ""
}
