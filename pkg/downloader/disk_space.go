package downloader

import (
	"errors"
	"strings"
	"syscall"
)

const diskSpaceMessage = "磁盘空间不足，请清理磁盘空间后重试"

func isDiskSpaceError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) || isNativeDiskSpaceError(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no space left") || strings.Contains(message, "disk full") ||
		strings.Contains(message, "not enough space on the disk") || strings.Contains(message, "disk quota exceeded") ||
		strings.Contains(message, "磁盘空间不足") || strings.Contains(message, "磁盘空间不够") ||
		strings.Contains(message, "磁盘的空间不足")
}

func fileFailureMessage(stage string, err error) string {
	if isDiskSpaceError(err) {
		return diskSpaceMessage
	}
	return stage + ": " + err.Error()
}
