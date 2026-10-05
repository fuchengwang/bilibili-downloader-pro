package update

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseVersion accepts stable X.Y.Z versions and an optional leading v.
// Components are compared numerically; prerelease channels are intentionally absent.
func ParseVersion(value string) (string, [3]uint32, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	var numbers [3]uint32
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return "", numbers, fmt.Errorf("版本号请使用 1.2.3 格式")
	}
	for i, part := range parts {
		if part == "" || len(part) > 10 || (len(part) > 1 && part[0] == '0') {
			return "", numbers, fmt.Errorf("版本号请使用 1.2.3 格式，数字不能有前导零")
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return "", numbers, fmt.Errorf("目前仅支持 1.2.3 格式的正式版本")
			}
		}
		n, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return "", numbers, fmt.Errorf("版本号数字过大")
		}
		numbers[i] = uint32(n)
	}
	return value, numbers, nil
}

func ValidPlatform(os, arch string, allowUniversal bool) bool {
	if os != "windows" && os != "darwin" && os != "linux" {
		return false
	}
	return arch == "amd64" || arch == "arm64" || (allowUniversal && os == "darwin" && arch == "universal")
}
