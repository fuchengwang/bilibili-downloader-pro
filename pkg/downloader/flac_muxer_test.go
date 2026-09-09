package downloader

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
)

// FFprobeOutput 用于解析 ffprobe 严格输出结构
type FFprobeOutput struct {
	Streams []struct {
		Index     int    `json:"index"`
		CodecName string `json:"codec_name"`
		CodecType string `json:"codec_type"`
		Duration  string `json:"duration"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
		Size     string `json:"size"`
	} `json:"format"`
}

func findGoModuleDir(pkgName string) string {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", pkgName)
	if out, err := cmd.Output(); err == nil {
		dir := strings.TrimSpace(string(out))
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
	}
	return ""
}

// TestRawFlacConversionAndMuxingMatrix 批量测试各种采样率、位深、声道与 ID3 标签的原生 FLAC 音频转换与合成
func TestRawFlacConversionAndMuxingMatrix(t *testing.T) {
	flacModDir := findGoModuleDir("github.com/mewkiz/flac")
	if flacModDir == "" {
		flacModDir = "/Users/fucheng/Tools/go/pkg/mod/github.com/mewkiz/flac@v1.0.14"
	}
	flacDir := filepath.Join(flacModDir, "testdata")
	if _, err := os.Stat(flacDir); os.IsNotExist(err) {
		t.Skip("FLAC testdata directory not found, skipping")
	}

	mp4ffModDir := findGoModuleDir("github.com/Eyevinn/mp4ff")
	if mp4ffModDir == "" {
		mp4ffModDir = "/Users/fucheng/Tools/go/pkg/mod/github.com/!eyevinn/mp4ff@v0.55.0"
	}
	videoSample := filepath.Join(mp4ffModDir, "examples", "multitrack", "testdata", "main_1.mp4")
	if _, err := os.Stat(videoSample); os.IsNotExist(err) {
		t.Skip("Video sample not found, skipping")
	}

	flacFiles, err := filepath.Glob(filepath.Join(flacDir, "*.flac"))
	if err != nil || len(flacFiles) == 0 {
		t.Skip("No flac test files found, skipping")
	}

	t.Logf("找到 %d 个不同规格的真实 FLAC 测试样本，开始全量矩阵校验...", len(flacFiles))

	ffprobePath, _ := exec.LookPath("ffprobe")

	for _, flacPath := range flacFiles {
		baseName := filepath.Base(flacPath)
		t.Run("FLAC_"+baseName, func(t *testing.T) {
			// 1. 测试 IsRawFlac 判定
			if !IsRawFlac(flacPath) {
				t.Fatalf("[%s] IsRawFlac 判定失败，预期为 true", baseName)
			}

			tmpDir := t.TempDir()
			fmp4AudioPath := filepath.Join(tmpDir, "converted_audio.m4s")
			mergedMp4Path := filepath.Join(tmpDir, "final_merged.mp4")

			// 2. 测试 PackageRawFlacToFMP4 独立转换
			err := PackageRawFlacToFMP4(flacPath, fmp4AudioPath)
			if err != nil {
				t.Fatalf("[%s] PackageRawFlacToFMP4 失败: %v", baseName, err)
			}

			fi, err := os.Stat(fmp4AudioPath)
			if err != nil || fi.Size() == 0 {
				t.Fatalf("[%s] 生成的 fMP4 音频文件为空", baseName)
			}

			// 3. 校验生成的 fMP4 音频结构
			f, err := os.Open(fmp4AudioPath)
			if err != nil {
				t.Fatalf("[%s] 打开 fMP4 失败: %v", baseName, err)
			}
			parsedAudio, err := mp4.DecodeFile(f)
			f.Close()
			if err != nil {
				t.Fatalf("[%s] mp4.DecodeFile 校验 fMP4 音频失败: %v", baseName, err)
			}
			if parsedAudio.Moov == nil || len(parsedAudio.Moov.Traks) == 0 {
				t.Fatalf("[%s] fMP4 缺少 moov 或 trak", baseName)
			}
			audioTrak := parsedAudio.Moov.Traks[0]
			if audioTrak.Mdia.Hdlr.HandlerType != "soun" {
				t.Fatalf("[%s] 预期音轨 handler 为 'soun'，实际为: %s", baseName, audioTrak.Mdia.Hdlr.HandlerType)
			}

			// 4. 测试与视频文件的真实合并
			err = MergeAudioVideo(videoSample, flacPath, mergedMp4Path, false)
			if err != nil {
				t.Fatalf("[%s] MergeAudioVideo 失败: %v", baseName, err)
			}

			outFi, err := os.Stat(mergedMp4Path)
			if err != nil || outFi.Size() == 0 {
				t.Fatalf("[%s] 合成最终 MP4 文件为空", baseName)
			}

			// 5. 校验最终 MP4 的双轨道与分片结构
			mergedF, err := os.Open(mergedMp4Path)
			if err != nil {
				t.Fatalf("[%s] 打开合成文件失败: %v", baseName, err)
			}
			parsedMerged, err := mp4.DecodeFile(mergedF)
			mergedF.Close()
			if err != nil {
				t.Fatalf("[%s] mp4.DecodeFile 校验最终 MP4 失败: %v", baseName, err)
			}

			if parsedMerged.Moov == nil || len(parsedMerged.Moov.Traks) < 2 {
				t.Fatalf("[%s] 最终 MP4 轨道数量不足 2，实际为: %d", baseName, len(parsedMerged.Moov.Traks))
			}

			track1Handler := parsedMerged.Moov.Traks[0].Mdia.Hdlr.HandlerType
			track2Handler := parsedMerged.Moov.Traks[1].Mdia.Hdlr.HandlerType

			t.Logf("✓ [%s] 纯 Go 合成成功: 大小=%.2f KB, Track1=%s, Track2=%s",
				baseName, float64(outFi.Size())/1024, track1Handler, track2Handler)

			// 6. 如果系统装有 ffprobe，使用工业级播放校验工具进行无报错严格检测
			if ffprobePath != "" {
				cmd := exec.Command(ffprobePath, "-v", "error", "-show_entries",
					"stream=index,codec_name,codec_type,duration:format=duration,size",
					"-of", "json", mergedMp4Path)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("[%s] ffprobe 严格校验报错: %v, 输出: %s", baseName, err, string(out))
				}

				var probe FFprobeOutput
				if err := json.Unmarshal(out, &probe); err != nil {
					t.Fatalf("[%s] 解析 ffprobe json 失败: %v", baseName, err)
				}

				if len(probe.Streams) < 2 {
					t.Fatalf("[%s] ffprobe 检测到的流少于 2", baseName)
				}
				t.Logf("✓ [%s] ffprobe 严格格式校验 0 错误通过: Stream0=%s(%s), Stream1=%s(%s)",
					baseName,
					probe.Streams[0].CodecName, probe.Streams[0].CodecType,
					probe.Streams[1].CodecName, probe.Streams[1].CodecType,
				)
			}
		})
	}
}

// TestCorruptAndInvalidAudioCases 测试损坏或非 FLAC 音频文件的容错与防御能力
func TestCorruptAndInvalidAudioCases(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. 虚假 FLAC 头但内容不全
	fakeFlac := filepath.Join(tmpDir, "fake.flac")
	_ = os.WriteFile(fakeFlac, []byte("fLaCTRUNCATED_DATA_WITHOUT_STREAMINFO"), 0644)

	outFmp4 := filepath.Join(tmpDir, "out.m4s")
	err := PackageRawFlacToFMP4(fakeFlac, outFmp4)
	if err == nil {
		t.Fatal("损坏的 FLAC 数据应该返回错误，但返回了 nil")
	}
	t.Logf("✓ 损坏 FLAC 防御校验通过: %v", err)

	// 2. 非 FLAC 文件
	nonFlac := filepath.Join(tmpDir, "not_flac.txt")
	_ = os.WriteFile(nonFlac, []byte("PLAIN_TEXT_FILE_CONTENT"), 0644)
	if IsRawFlac(nonFlac) {
		t.Fatal("文本文件不应被误判为 FLAC")
	}

	err = PackageRawFlacToFMP4(nonFlac, outFmp4)
	if err == nil {
		t.Fatal("非 FLAC 文件应该返回错误，但返回了 nil")
	}
	t.Logf("✓ 非 FLAC 文件拒绝校验通过: %v", err)

	// 3. 空音频路径直接复制视频测试
	mp4ffModDir := findGoModuleDir("github.com/Eyevinn/mp4ff")
	if mp4ffModDir == "" {
		mp4ffModDir = "/Users/fucheng/Tools/go/pkg/mod/github.com/!eyevinn/mp4ff@v0.55.0"
	}
	videoSample := filepath.Join(mp4ffModDir, "examples", "multitrack", "testdata", "main_1.mp4")
	if _, err := os.Stat(videoSample); err == nil {
		outOnlyVideo := filepath.Join(tmpDir, "only_video.mp4")
		err := MergeAudioVideo(videoSample, "", outOnlyVideo, false)
		if err != nil {
			t.Fatalf("空音频合并视频失败: %v", err)
		}
		fi, _ := os.Stat(outOnlyVideo)
		if fi.Size() == 0 {
			t.Fatal("生成文件为空")
		}
		t.Logf("✓ 空音频直通单视频流测试通过，大小: %.2f KB", float64(fi.Size())/1024)
	}
}
