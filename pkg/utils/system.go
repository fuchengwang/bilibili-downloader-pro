package utils

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// illegalChars 匹配 Windows / macOS / Linux 文件系统非法字符及 ASCII 控制字符
var (
	illegalChars      = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f\x7f\r\n\t]`)
	invisibleChars    = regexp.MustCompile(`[\x{200B}-\x{200D}\x{FEFF}\x{200E}\x{200F}\x{202A}-\x{202E}]`)
	consecutiveSpaces = regexp.MustCompile(`[ \t]+`)
	consecutiveUnders = regexp.MustCompile(`_+`)
)

// windowsReservedNames Windows 设备保留名称集合（不区分大小写）
var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SanitizeFilename 清理并规范化文件名与目录名（跨平台兼容 macOS / Windows / Linux）
// 1. 过滤非法字符与不可见/零宽控制字符
// 2. 避免 Windows 保留设备名称冲突 (CON, PRN, AUX, NUL, COM1-9, LPT1-9 等)
// 3. 去除前后空格与末尾句点（Windows 不允许以句点或空格结尾）
// 4. 限制长度在安全阈值内（80 字符 / 180 字节，预留后缀空间，按 Rune 截断防乱码）
// 5. 极端情况下提供安全降级回退
func SanitizeFilename(name string, fallback ...string) string {
	fallbackName := "video"
	if len(fallback) > 0 && strings.TrimSpace(fallback[0]) != "" {
		fallbackName = strings.TrimSpace(fallback[0])
	}

	// 1. 移除不可见字符与控制字符
	clean := invisibleChars.ReplaceAllString(name, "")

	// 2. 替换文件系统非法字符为下划线
	clean = illegalChars.ReplaceAllString(clean, "_")

	// 3. 折叠多余的连续下划线与空格
	clean = consecutiveUnders.ReplaceAllString(clean, "_")
	clean = consecutiveSpaces.ReplaceAllString(clean, " ")

	// 4. 去除首尾空白字符与句点/下划线（Windows 不允许文件名以点或空格结尾）
	clean = strings.Trim(clean, " .\t\r\n_")

	// 5. 长度限制与字符安全截断（最大 80 个字符且 <= 180 字节，防止超长导致路径溢出）
	const maxRunes = 80
	const maxBytes = 180

	runes := []rune(clean)
	if len(runes) > maxRunes {
		clean = string(runes[:maxRunes])
	}
	for len([]byte(clean)) > maxBytes {
		runes = []rune(clean)
		if len(runes) <= 1 {
			break
		}
		clean = string(runes[:len(runes)-1])
	}
	clean = strings.Trim(clean, " .\t\r\n_")

	// 6. Windows 保留设备名称安全防御 (如 CON, AUX, NUL 等)
	upper := strings.ToUpper(clean)
	baseWithoutExt := upper
	if dotIdx := strings.Index(upper, "."); dotIdx > 0 {
		baseWithoutExt = upper[:dotIdx]
	}
	if windowsReservedNames[upper] || windowsReservedNames[baseWithoutExt] {
		clean = "_" + clean
	}

	// 7. 降级回退保护：如果清洗后为空或仅剩非法符号，使用安全的默认回退名称
	if clean == "" {
		if fallbackName != "video" {
			return SanitizeFilename(fallbackName)
		}
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
	target, err := resolveOpenDirectoryTarget(path)
	if err != nil {
		return err
	}
	return openDirectoryOS(target)
}

func resolveOpenDirectoryTarget(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("路径为空")
	}

	cleanPath := filepath.Clean(path)
	if _, err := os.Stat(cleanPath); err == nil {
		return cleanPath, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("无法访问文件或目录: %w", err)
	}

	dir := filepath.Dir(cleanPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("下载目录不存在或无法访问: %w", err)
	}
	base := strings.TrimSuffix(filepath.Base(cleanPath), ".mp4")
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), base) && strings.Contains(e.Name(), ".downloading") {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	// 文件已移动或删除时打开已有的父目录，不创建空目录。
	return dir, nil
}

// OpenFile 使用系统默认播放器打开/播放指定文件
func OpenFile(path string) error {
	if path == "" {
		return fmt.Errorf("文件路径为空")
	}
	return openFileOS(path)
}

// SafeRemoveWithRetry 带指数退避重试的文件安全删除函数 (专为 Windows 句柄延迟释放防御设计)
func SafeRemoveWithRetry(filePath string) {
	if filePath == "" {
		return
	}
	for i := 0; i < 5; i++ {
		err := os.Remove(filePath)
		if err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(time.Duration(100*(1<<i)) * time.Millisecond)
	}
}

// AtomicWriteFile 跨平台高可靠原子文件写入 (完美防御 Windows os.Rename 权限拒绝与冲突)
func AtomicWriteFile(filePath string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".tmp_atomic-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanupTemp := true
	defer func() {
		_ = tmp.Close()
		if cleanupTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	written, err := tmp.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	// 1. 优先尝试直接原子重命名；同卷 Unix 覆盖和目标不存在时均是原子的。
	if err := os.Rename(tmpPath, filePath); err == nil {
		cleanupTemp = false
		return nil
	}

	// 2. Windows 目标已存在时不能直接 Rename。先把旧文件移到同目录备份，
	// 新文件替换失败则立即恢复；旧文件从未在新内容准备好前被删除。
	info, statErr := os.Stat(filePath)
	if statErr != nil {
		return fmt.Errorf("原子替换失败: %w", statErr)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("原子写入目标不是普通文件: %s", filePath)
	}

	backup, err := os.CreateTemp(dir, ".tmp_atomic-backup-*")
	if err != nil {
		return err
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		return err
	}
	if err := os.Rename(filePath, backupPath); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		if restoreErr := os.Rename(backupPath, filePath); restoreErr != nil {
			return fmt.Errorf("原子替换失败: %w；恢复旧文件也失败: %v", err, restoreErr)
		}
		return err
	}
	cleanupTemp = false
	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("删除旧配置备份失败: %w", err)
	}
	return nil
}

// EnsureSafePathLength 确保在 Windows MAX_PATH (260字符) 限制下，全路径保持在 210 字符安全阈值内，为后续临时文件或画质标签预留充分空间
func EnsureSafePathLength(dir, filename, ext string) string {
	const maxChars = 210
	dirRunes := len([]rune(dir))
	extRunes := len([]rune(ext))

	maxNameRunes := maxChars - dirRunes - 1 - extRunes
	if maxNameRunes < 10 {
		maxNameRunes = 10
	}

	runes := []rune(filename)
	if len(runes) > maxNameRunes {
		runes = runes[:maxNameRunes]
		filename = strings.Trim(string(runes), " ._-")
		if filename == "" {
			filename = "video"
		}
	}
	return filepath.Join(dir, filename+ext)
}
