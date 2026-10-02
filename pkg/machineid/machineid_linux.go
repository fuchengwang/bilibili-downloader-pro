//go:build linux

package machineid

import (
	"os"
	"strings"
)

// getRawMachineID 获取 Linux 平台的机器码
func getRawMachineID() (string, error) {
	// 方案1: /etc/machine-id (systemd 系统标准)
	if data, err := os.ReadFile("/etc/machine-id"); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
	}

	// 方案2: /var/lib/dbus/machine-id (非 systemd 系统)
	if data, err := os.ReadFile("/var/lib/dbus/machine-id"); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
	}

	// 方案3: /sys/class/dmi/id/product_uuid
	if data, err := os.ReadFile("/sys/class/dmi/id/product_uuid"); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id, nil
		}
	}

	return getFallbackID(), nil
}
