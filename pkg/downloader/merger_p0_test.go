package downloader

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestScanBoxes_Corrupted64BitLargeSize_PreventInfiniteLoop 测试当 64 位 Box 的 largesize 超大（导致 int64 符号溢出为负数）时，
// scanBoxes 必须能够安全截断退出，绝对不能陷入 100% CPU 无限死循环。
func TestScanBoxes_Corrupted64BitLargeSize_PreventInfiniteLoop(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "corrupted_largesize.mp4")

	// 构造一个包含 64-bit largesize Box 的损坏 MP4 文件
	// 4字节 size=1 (表示使用 64位 largesize), 4字节 "mdat", 8字节 largesize = 0xFFFFFFFFFFFFFF00 (高位为 1，强转 int64 为负数)
	raw := make([]byte, 32)
	raw[3] = 1 // size = 1
	copy(raw[4:8], "mdat")
	// 写入一个会导致 int64(size) < 0 的极大 64位无符号整数
	raw[8] = 0xFF
	raw[9] = 0xFF
	raw[10] = 0xFF
	raw[11] = 0xFF
	raw[12] = 0xFF
	raw[13] = 0xFF
	raw[14] = 0xFF
	raw[15] = 0x00

	if err := os.WriteFile(srcFile, raw, 0644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}

	f, err := os.Open(srcFile)
	if err != nil {
		t.Fatalf("打开测试文件失败: %v", err)
	}
	defer f.Close()

	done := make(chan struct{})
	var boxes []BoxInfo
	var scanErr error

	go func() {
		boxes, scanErr = scanBoxes(f, int64(len(raw)))
		close(done)
	}()

	select {
	case <-done:
		if scanErr != nil {
			t.Logf("scanBoxes 正确识别并返回错误: %v", scanErr)
		}
		// 校验返回的 box offset 不应为负数
		for _, b := range boxes {
			if b.Offset < 0 {
				t.Fatalf("严重缺陷：Box 偏移量为负数 (%d)", b.Offset)
			}
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("严重缺陷：scanBoxes 陷入死循环！未能及时退出！")
	}
}

// TestScanBoxes_ValidPrecedingBoxesWithTrailingCorruptedLargeSize 测试前面包含正常 Box，末尾包含损坏超大 Box 的场景
func TestScanBoxes_ValidPrecedingBoxesWithTrailingCorruptedLargeSize(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "mixed_boxes.mp4")

	// 1. 第一个 Box: ftyp (16 字节)
	ftyp := make([]byte, 16)
	binary.BigEndian.PutUint32(ftyp[0:4], 16)
	copy(ftyp[4:8], "ftyp")
	copy(ftyp[8:12], "isom")

	// 2. 第二个 Box: 损坏的 64 位 largesize Box
	corrupted := make([]byte, 16)
	corrupted[3] = 1 // size = 1
	copy(corrupted[4:8], "mdat")
	corrupted[8] = 0x80 // 导致有符号 int64 为负数

	raw := append(ftyp, corrupted...)

	if err := os.WriteFile(srcFile, raw, 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	f, err := os.Open(srcFile)
	if err != nil {
		t.Fatalf("打开测试文件失败: %v", err)
	}
	defer f.Close()

	boxes, err := scanBoxes(f, int64(len(raw)))
	if err == nil {
		t.Fatal("scanBoxes 遇到尾部损坏应返回错误，避免合并器把截断文件当作完整输入")
	}

	// 必须能够正确解析出第一个合法的 ftyp Box
	if len(boxes) != 1 {
		t.Fatalf("期望正确解析出 1 个合法 Box，实际解析出: %d 个", len(boxes))
	}
	if boxes[0].Type != "ftyp" || boxes[0].Size != 16 {
		t.Errorf("首个 Box 解析不准确: %+v", boxes[0])
	}
}

// TestScanBoxes_MaxUint64Size 测试 size 为 math.MaxUint64 的极值边界情况
func TestScanBoxes_MaxUint64Size(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "max_uint64_size.mp4")

	raw := make([]byte, 16)
	raw[3] = 1
	copy(raw[4:8], "moov")
	for i := 8; i < 16; i++ {
		raw[i] = 0xFF // uint64(math.MaxUint64)
	}

	if err := os.WriteFile(srcFile, raw, 0644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}

	f, err := os.Open(srcFile)
	if err != nil {
		t.Fatalf("打开测试文件失败: %v", err)
	}
	defer f.Close()

	done := make(chan struct{})
	go func() {
		_, _ = scanBoxes(f, int64(len(raw)))
		close(done)
	}()

	select {
	case <-done:
		// 正常安全退出
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("scanBoxes 遇到 MaxUint64 size 陷入死循环！")
	}
}

// TestScanBoxes_ZeroAndNegativeFileSize 测试空文件或负数 fileSize 时的安全防御
func TestScanBoxes_ZeroAndNegativeFileSize(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "empty.mp4")
	_ = os.WriteFile(srcFile, []byte{}, 0644)

	f, err := os.Open(srcFile)
	if err != nil {
		t.Fatalf("打开测试文件失败: %v", err)
	}
	defer f.Close()

	boxes, err := scanBoxes(f, 0)
	if err != nil || len(boxes) != 0 {
		t.Errorf("空文件期望返回 (0, nil)，实际得到 len=%d, err=%v", len(boxes), err)
	}

	boxesNeg, errNeg := scanBoxes(f, -100)
	if errNeg == nil || len(boxesNeg) != 0 {
		t.Errorf("负文件大小应安全返回错误，实际得到 len=%d, err=%v", len(boxesNeg), errNeg)
	}
}

// TestCopyOrRename_OverwriteExistingDestination 测试在目标文件已存在时，
// copyOrRename 能够安全且高效地覆盖目标文件，并将源文件成功移除
func TestCopyOrRename_OverwriteExistingDestination(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "source.mp4")
	dstPath := filepath.Join(tmpDir, "destination.mp4")

	srcContent := []byte("new source video stream content 2026")
	dstContentOld := []byte("old destination content to be overwritten")

	if err := os.WriteFile(srcPath, srcContent, 0644); err != nil {
		t.Fatalf("写入源测试文件失败: %v", err)
	}
	if err := os.WriteFile(dstPath, dstContentOld, 0644); err != nil {
		t.Fatalf("写入旧目标文件失败: %v", err)
	}

	err := copyOrRename(srcPath, dstPath)
	if err != nil {
		t.Fatalf("copyOrRename 失败: %v", err)
	}

	// 1. 验证源文件已被移走/删除
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Errorf("copyOrRename 完成后源文件依然残留！")
	}

	// 2. 验证目标文件已被正确更新为新内容
	actualDstContent, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("读取目标文件失败: %v", err)
	}
	if !bytes.Equal(actualDstContent, srcContent) {
		t.Errorf("目标文件内容不匹配: 期望 %s, 实际 %s", string(srcContent), string(actualDstContent))
	}
}

func TestWriteMdatBoxRejectsTruncatedPayload(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "short.mdat")
	// 文件实际只有 4 字节 payload，但 BoxInfo 宣称有 12 字节 payload。
	raw := append([]byte{0, 0, 0, 12, 'm', 'd', 'a', 't'}, []byte("data")...)
	if err := os.WriteFile(srcPath, raw, 0644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var out bytes.Buffer
	err = writeMdatBox(f, BoxInfo{Type: "mdat", Offset: 0, Size: 20, HeaderLen: 8}, &out, make([]byte, 128))
	if err == nil {
		t.Fatal("mdat payload 被截断时不应返回成功")
	}
}

func TestExtractFragmentsRejectsUndecodableMoof(t *testing.T) {
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "bad.moof")
	raw := []byte{0, 0, 0, 8, 'm', 'o', 'o', 'f'}
	if err := os.WriteFile(srcPath, raw, 0644); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	boxes, err := scanBoxes(f, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extractFragments(boxes, f, false, 1000); err == nil {
		t.Fatal("无法解码或缺少 traf 的 moof 不应被静默跳过")
	}
}

func TestCopyOrRenameFailurePreservesExistingDestination(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "source-directory")
	dstPath := filepath.Join(tmpDir, "existing.mp4")
	if err := os.Mkdir(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	original := []byte("old complete output")
	if err := os.WriteFile(dstPath, original, 0644); err != nil {
		t.Fatal(err)
	}

	if err := copyOrRename(srcDir, dstPath); err == nil {
		t.Fatal("目录作为源文件时应返回复制错误")
	}
	actual, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, original) {
		t.Fatalf("复制失败时不应破坏旧成品: got %q, want %q", actual, original)
	}
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".bbdown-") {
			t.Fatalf("复制失败后不应遗留替换临时文件: %s", entry.Name())
		}
	}
}

func TestMergeAudioVideoWithoutDeleteKeepsVideoSource(t *testing.T) {
	tmpDir := t.TempDir()
	videoPath := filepath.Join(tmpDir, "video.m4s")
	outputPath := filepath.Join(tmpDir, "output.mp4")
	content := []byte("video input that must remain when deleteTemp is false")
	if err := os.WriteFile(videoPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	if err := MergeAudioVideo(videoPath, "", outputPath, false); err != nil {
		t.Fatalf("无音频直通复制失败: %v", err)
	}
	if actual, err := os.ReadFile(videoPath); err != nil {
		t.Fatalf("deleteTemp=false 时源文件不应被移除: %v", err)
	} else if !bytes.Equal(actual, content) {
		t.Fatalf("源文件内容被改变: got %q, want %q", actual, content)
	}
	if actual, err := os.ReadFile(outputPath); err != nil {
		t.Fatal(err)
	} else if !bytes.Equal(actual, content) {
		t.Fatalf("输出内容不一致: got %q, want %q", actual, content)
	}
}
