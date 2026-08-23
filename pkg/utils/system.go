package utils

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var illegalChars = regexp.MustCompile(`[\\/:*?"<>|\r\n\t]`)

// SanitizeFilename 清理文件名中的非法字符
func SanitizeFilename(name string) string {
	clean := illegalChars.ReplaceAllString(name, "_")
	clean = strings.TrimSpace(clean)
	// 避免文件名过长
	if len([]rune(clean)) > 150 {
		runes := []rune(clean)
		clean = string(runes[:150])
	}
	if clean == "" {
		return "video"
	}
	return clean
}

// FormatBytes 格式化字节大小为人类可读格式 (e.g. 15.2 MB)
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// FormatSpeed 格式化下载速度 (e.g. 4.2 MB/s)
func FormatSpeed(bytesPerSec int64) string {
	if bytesPerSec <= 0 {
		return "0 KB/s"
	}
	return FormatBytes(bytesPerSec) + "/s"
}

// FormatDuration 格式化秒数为 01:23:45 格式
func FormatDuration(sec int) string {
	if sec <= 0 {
		return "00:00"
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// OpenDirectory 在系统文件管理器中打开指定目录或高亮文件
func OpenDirectory(path string) error {
	if path == "" {
		return fmt.Errorf("路径为空")
	}

	cleanPath := filepath.Clean(path)

	targetToOpen := cleanPath
	if _, err := os.Stat(cleanPath); err != nil {
		dir := filepath.Dir(cleanPath)
		base := strings.TrimSuffix(filepath.Base(cleanPath), ".mp4")
		matched := ""
		if entries, rErr := os.ReadDir(dir); rErr == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), base) && strings.Contains(e.Name(), ".downloading") {
					matched = filepath.Join(dir, e.Name())
					break
				}
			}
		}
		if matched != "" {
			targetToOpen = matched
		} else {
			_ = os.MkdirAll(dir, 0755)
			targetToOpen = dir
		}
	}

	return openDirectoryOS(targetToOpen)
}

// OpenFile 使用系统默认播放器打开/播放指定文件
func OpenFile(path string) error {
	if path == "" {
		return fmt.Errorf("文件路径为空")
	}
	return openFileOS(path)
}

// FFmpegVersionInfo 获取 FFmpeg 版本信息
func FFmpegVersionInfo(ffmpegPath string) (bool, string, error) {
	if ffmpegPath == "" {
		return false, "未配置 FFmpeg 路径", fmt.Errorf("ffmpeg path is empty")
	}
	out, err := exec.Command(ffmpegPath, "-version").CombinedOutput()
	if err != nil {
		return false, string(out), err
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) > 0 {
		return true, strings.TrimSpace(lines[0]), nil
	}
	return true, "FFmpeg 已就绪", nil
}
