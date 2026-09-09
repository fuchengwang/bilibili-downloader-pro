package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSanitizeFilename_IllegalCharacters 测试非法字符替换
func TestSanitizeFilename_IllegalCharacters(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"【4K/60帧】视频:标题?|*<测试>", "【4K_60帧】视频_标题_测试"},
		{"line1\r\nline2\tline3\x00end", "line1_line2_line3_end"},
		{"hello/world\\test", "hello_world_test"},
		{"a|b*c?d:e\"f<g>h", "a_b_c_d_e_f_g_h"},
	}

	for _, c := range cases {
		actual := SanitizeFilename(c.input)
		if actual != c.expected {
			t.Errorf("SanitizeFilename(%q) = %q, expected %q", c.input, actual, c.expected)
		}
	}
}

// TestSanitizeFilename_WindowsReservedNames 测试 Windows 设备保留名称防御 (CON, PRN, AUX, NUL, COM1-9, LPT1-9 等)
func TestSanitizeFilename_WindowsReservedNames(t *testing.T) {
	reserved := []string{
		"con", "CON", "aux", "AUX", "prn", "PRN", "nul", "NUL",
		"com1", "COM9", "lpt1", "LPT9",
		"con.mp4", "AUX.1080P", "nul.part1",
	}

	for _, name := range reserved {
		sanitized := SanitizeFilename(name)
		upper := strings.ToUpper(sanitized)
		base := upper
		if dotIdx := strings.Index(upper, "."); dotIdx > 0 {
			base = upper[:dotIdx]
		}
		if windowsReservedNames[upper] || windowsReservedNames[base] {
			t.Errorf("SanitizeFilename(%q) = %q, still violates Windows reserved device name!", name, sanitized)
		}
		if !strings.HasPrefix(sanitized, "_") {
			t.Errorf("SanitizeFilename(%q) = %q, expected prefix '_'", name, sanitized)
		}
	}
}

// TestSanitizeFilename_LeadingTrailingDotsAndSpaces 测试首尾点号与空格去除 (Windows 禁止以点或空格结尾)
func TestSanitizeFilename_LeadingTrailingDotsAndSpaces(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"  ..video name..   ", "video name"},
		{"...episode 01...", "episode 01"},
		{"   ___part 2___   ", "part 2"},
		{".hidden.file.", "hidden.file"},
	}

	for _, c := range cases {
		actual := SanitizeFilename(c.input)
		if actual != c.expected {
			t.Errorf("SanitizeFilename(%q) = %q, expected %q", c.input, actual, c.expected)
		}
		if strings.HasSuffix(actual, ".") || strings.HasSuffix(actual, " ") {
			t.Errorf("SanitizeFilename output %q ends with dot or space!", actual)
		}
	}
}

// TestSanitizeFilename_LengthLimiting 测试超长文件名按 Rune 安全截断且绝不损坏 UTF-8 编码
func TestSanitizeFilename_LengthLimiting(t *testing.T) {
	longChinese := strings.Repeat("这是一个非常非常长的高清哔哩哔哩视频标题用于测试文件系统长度限制", 10)
	sanitized := SanitizeFilename(longChinese)

	if !utf8.ValidString(sanitized) {
		t.Fatalf("Sanitized string is invalid UTF-8 encoding!")
	}

	runeCount := len([]rune(sanitized))
	byteCount := len([]byte(sanitized))

	if runeCount > 80 {
		t.Errorf("Rune count %d exceeds max limit 80", runeCount)
	}
	if byteCount > 180 {
		t.Errorf("Byte count %d exceeds max limit 180", byteCount)
	}
}

// TestSanitizeFilename_InvisibleAndZeroWidthChars 测试不可见控制字符与零宽字符过滤
func TestSanitizeFilename_InvisibleAndZeroWidthChars(t *testing.T) {
	// 包含 \u200B (零宽空格), \uFEFF (BOM), \u202E (从右向左覆盖)
	input := "正常标题\u200B带\uFEFF零宽\u202E字符"
	expected := "正常标题带零宽字符"

	actual := SanitizeFilename(input)
	if actual != expected {
		t.Errorf("SanitizeFilename(%q) = %q, expected %q", input, actual, expected)
	}
}

// TestSanitizeFilename_Fallbacks 测试空串与全非法字符的降级回退保护
func TestSanitizeFilename_Fallbacks(t *testing.T) {
	cases := []struct {
		input    string
		fallback string
		expected string
	}{
		{"", "", "video"},
		{"   ", "P01", "P01"},
		{":::***???///", "custom_fallback", "custom_fallback"},
		{"....____....", "", "video"},
	}

	for _, c := range cases {
		var actual string
		if c.fallback != "" {
			actual = SanitizeFilename(c.input, c.fallback)
		} else {
			actual = SanitizeFilename(c.input)
		}
		if actual != c.expected {
			t.Errorf("SanitizeFilename(%q, fallback=%q) = %q, expected %q", c.input, c.fallback, actual, c.expected)
		}
	}
}

// TestFormatBytes 测试字节大小格式化转换
func TestFormatBytes(t *testing.T) {
	cases := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.00 KB"},
		{1536, "1.50 KB"},
		{1048576, "1.00 MB"},
		{1073741824, "1.00 GB"},
	}

	for _, c := range cases {
		actual := FormatBytes(c.bytes)
		if actual != c.expected {
			t.Errorf("FormatBytes(%d) = %q, expected %q", c.bytes, actual, c.expected)
		}
	}
}

// TestFormatSpeed 测试下载速度格式化
func TestFormatSpeed(t *testing.T) {
	if FormatSpeed(0) != "0 KB/s" {
		t.Errorf("FormatSpeed(0) = %q, expected '0 KB/s'", FormatSpeed(0))
	}
	if FormatSpeed(-100) != "0 KB/s" {
		t.Errorf("FormatSpeed(-100) = %q, expected '0 KB/s'", FormatSpeed(-100))
	}
	if FormatSpeed(1048576) != "1.00 MB/s" {
		t.Errorf("FormatSpeed(1048576) = %q, expected '1.00 MB/s'", FormatSpeed(1048576))
	}
}

// TestFormatDuration 测试时间秒数转 00:00:00
func TestFormatDuration(t *testing.T) {
	cases := []struct {
		sec      int
		expected string
	}{
		{0, "00:00"},
		{-10, "00:00"},
		{45, "00:45"},
		{75, "01:15"},
		{3665, "01:01:05"},
	}

	for _, c := range cases {
		actual := FormatDuration(c.sec)
		if actual != c.expected {
			t.Errorf("FormatDuration(%d) = %q, expected %q", c.sec, actual, c.expected)
		}
	}
}

// TestAtomicWriteFile 测试原子写入与覆盖更新
func TestAtomicWriteFile(t *testing.T) {
	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "atomic_test.txt")

	data1 := []byte("Initial Data Content 1")
	if err := AtomicWriteFile(targetPath, data1, 0644); err != nil {
		t.Fatalf("第一次写入失败: %v", err)
	}

	readBack1, err := os.ReadFile(targetPath)
	if err != nil || string(readBack1) != string(data1) {
		t.Fatalf("读取内容不符: %v, 内容: %s", err, string(readBack1))
	}

	// 模拟覆盖已有文件
	data2 := []byte("Overwritten Data Content 2 with new length")
	if err := AtomicWriteFile(targetPath, data2, 0644); err != nil {
		t.Fatalf("覆盖写入失败: %v", err)
	}

	readBack2, err := os.ReadFile(targetPath)
	if err != nil || string(readBack2) != string(data2) {
		t.Fatalf("覆盖后内容不符: %v, 内容: %s", err, string(readBack2))
	}
}

// TestEnsureSafePathLength 测试 Windows 240 字符超长路径安全截断
func TestEnsureSafePathLength(t *testing.T) {
	dir := "/Users/test/Downloads/Bilibili/very_long_collection_directory_name_that_takes_up_space"
	ext := ".mp4"

	// 短文件名不需要截断
	shortName := "P01_intro"
	safeShort := EnsureSafePathLength(dir, shortName, ext)
	if !strings.HasSuffix(safeShort, ext) {
		t.Errorf("期望后缀为 %s, 实际: %s", ext, safeShort)
	}

	// 超长文件名应当被安全截断至 <= 220 字符 (Windows MAX_PATH 安全阈值)
	superLongName := strings.Repeat("这是一个超长分P视频标题测试内容", 20)
	safeLong := EnsureSafePathLength(dir, superLongName, ext)
	if len([]rune(safeLong)) > 220 {
		t.Errorf("截断后路径字符数仍超过 220 字符: %d", len([]rune(safeLong)))
	}
	if !strings.HasSuffix(safeLong, ext) {
		t.Errorf("截断后丢失后缀: %s", safeLong)
	}
}


