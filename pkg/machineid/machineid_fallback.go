//go:build !darwin && !windows && !linux

package machineid

// getRawMachineID 兜底平台机器码提取实现
func getRawMachineID() (string, error) {
	return getFallbackID(), nil
}
