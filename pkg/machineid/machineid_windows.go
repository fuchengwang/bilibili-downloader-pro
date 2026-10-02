//go:build windows

package machineid

import (
	"bytes"
	"os/exec"
	"strings"
)

// getRawMachineID 获取 Windows 平台的唯一机器码
func getRawMachineID() (string, error) {
	// 方案1: 读取注册表 MachineGuid (快速、无进程弹窗、极稳定)
	cmdReg := exec.Command("reg", "query", `HKEY_LOCAL_MACHINE\SOFTWARE\Microsoft\Cryptography`, "/v", "MachineGuid")
	var outReg bytes.Buffer
	cmdReg.Stdout = &outReg
	if err := cmdReg.Run(); err == nil {
		lines := strings.Split(outReg.String(), "\n")
		for _, line := range lines {
			if strings.Contains(line, "MachineGuid") {
				fields := strings.Fields(line)
				if len(fields) >= 3 {
					guid := strings.TrimSpace(fields[len(fields)-1])
					if guid != "" {
						return guid, nil
					}
				}
			}
		}
	}

	// 方案2: wmic csproduct get uuid
	cmdWmic := exec.Command("wmic", "csproduct", "get", "uuid")
	var outWmic bytes.Buffer
	cmdWmic.Stdout = &outWmic
	if err := cmdWmic.Run(); err == nil {
		lines := strings.Split(outWmic.String(), "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.EqualFold(trimmed, "uuid") {
				return trimmed, nil
			}
		}
	}

	// 方案3: powershell Get-CimInstance
	cmdPS := exec.Command("powershell", "-NoProfile", "-Command", "(Get-CimInstance Win32_ComputerSystemProduct).UUID")
	var outPS bytes.Buffer
	cmdPS.Stdout = &outPS
	if err := cmdPS.Run(); err == nil {
		val := strings.TrimSpace(outPS.String())
		if val != "" {
			return val, nil
		}
	}

	return getFallbackID(), nil
}
