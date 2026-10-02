//go:build darwin

package machineid

import (
	"bytes"
	"os/exec"
	"regexp"
	"strings"
)

// getRawMachineID 获取 macOS 平台的硬件 UUID (IOPlatformUUID)
func getRawMachineID() (string, error) {
	// 执行 ioreg -rd1 -c IOPlatformExpertDevice
	cmd := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err == nil {
		re := regexp.MustCompile(`"IOPlatformUUID"\s*=\s*"([^"]+)"`)
		matches := re.FindStringSubmatch(out.String())
		if len(matches) > 1 && strings.TrimSpace(matches[1]) != "" {
			return strings.TrimSpace(matches[1]), nil
		}
	}

	// 备选方案: sysctl kern.uuid
	cmdSysctl := exec.Command("sysctl", "-n", "kern.uuid")
	var outSysctl bytes.Buffer
	cmdSysctl.Stdout = &outSysctl
	if err := cmdSysctl.Run(); err == nil {
		val := strings.TrimSpace(outSysctl.String())
		if val != "" {
			return val, nil
		}
	}

	return getFallbackID(), nil
}
