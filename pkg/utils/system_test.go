package utils

import (
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
