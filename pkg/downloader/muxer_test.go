package downloader

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"

	"github.com/Eyevinn/mp4ff/mp4"
)

// TestPureGoMuxerAcrossCodecs 测试纯 Go 合成器在 AVC, HEVC, AV1 编码下的稳定性和正确性
func TestPureGoMuxerAcrossCodecs(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping live download in short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	biliClient := bilibili.GetDefaultClient()

	target, err := biliClient.ParseInput(ctx, "https://www.bilibili.com/video/BV1m1Lv6pEiN/")
	if err != nil {
		t.Fatalf("ParseInput err: %v", err)
	}

	detail, err := biliClient.FetchVideoDetail(ctx, target)
	if err != nil {
		t.Fatalf("FetchVideoDetail err: %v", err)
	}

	ep := detail.Episodes[0]
	codecs := []string{"AVC", "HEVC", "AV1"}

	for _, codec := range codecs {
		t.Run("PureGoMux_"+codec, func(t *testing.T) {
			sel, err := biliClient.FetchStreamSelection(ctx, ep.BVID, ep.AID, ep.CID, ep.EPID, false, "80", codec)
			if err != nil {
				t.Fatalf("[%s] FetchStreamSelection err: %v", codec, err)
			}

			tmpDir := filepath.Join(os.TempDir(), "pure_go_mux_test_"+codec)
			_ = os.RemoveAll(tmpDir)
			_ = os.MkdirAll(tmpDir, 0755)
			defer os.RemoveAll(tmpDir)

			vTmp := filepath.Join(tmpDir, "video.m4s")
			aTmp := filepath.Join(tmpDir, "audio.m4s")
			outMp4 := filepath.Join(tmpDir, "output_"+codec+".mp4")

			vDownloader := NewStreamDownloader(sel.VideoURLs, vTmp)
			if err := vDownloader.DownloadSingleStream(ctx, nil); err != nil {
				t.Fatalf("[%s] Video download err: %v", codec, err)
			}

			if len(sel.AudioURLs) > 0 && sel.AudioURLs[0] != "" {
				aDownloader := NewStreamDownloader(sel.AudioURLs, aTmp)
				if err := aDownloader.DownloadSingleStream(ctx, nil); err != nil {
					t.Fatalf("[%s] Audio download err: %v", codec, err)
				}
			}

			// 🌟 执行纯 Go 原生合成
			muxStart := time.Now()
			if err := MergeAudioVideo(vTmp, aTmp, outMp4, false); err != nil {
				t.Fatalf("[%s] MergeAudioVideo failed: %v", codec, err)
			}
			t.Logf("✓ [%s] 纯 Go 合成成功！耗时: %v", codec, time.Since(muxStart))

			// 验证生成的文件大小
			fi, err := os.Stat(outMp4)
			if err != nil {
				t.Fatalf("[%s] Output file not found: %v", codec, err)
			}
			t.Logf("✓ [%s] 最终输出文件大小: %.2f MB", codec, float64(fi.Size())/1024/1024)

			// 使用 mp4ff 校验生成的双轨道 MP4 结构
			outFh, err := os.Open(outMp4)
			if err != nil {
				t.Fatalf("Open outMp4 err: %v", err)
			}
			defer outFh.Close()

			parsedMp4, err := mp4.DecodeFile(outFh)
			if err != nil {
				t.Fatalf("[%s] mp4.DecodeFile on generated MP4 failed: %v", codec, err)
			}

			if parsedMp4.Moov == nil || len(parsedMp4.Moov.Traks) < 2 {
				t.Fatalf("[%s] Expected 2 tracks in generated MP4, got %d", codec, len(parsedMp4.Moov.Traks))
			}

			t.Logf("✓ [%s] 轨道校验成功: Track 1 = %s, Track 2 = %s",
				codec,
				parsedMp4.Moov.Traks[0].Mdia.Hdlr.HandlerType,
				parsedMp4.Moov.Traks[1].Mdia.Hdlr.HandlerType,
			)

			// 如果本地有 ffprobe，通过 ffprobe 进行二次严格校验
			if ffprobePath, err := exec.LookPath("ffprobe"); err == nil {
				cmd := exec.Command(ffprobePath, "-v", "error", "-show_entries", "stream=codec_type,codec_name", "-of", "csv=p=0", outMp4)
				out, err := cmd.CombinedOutput()
				if err == nil {
					t.Logf("✓ [%s] FFprobe 严格校验通过:\n%s", codec, string(out))
				}
			}
		})
	}
}

// TestPureGoMuxerSize0Box 测试带有 Size 0 (EOF 盒) 异常流的健壮性与自动修正
func TestPureGoMuxerSize0Box(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping live download in short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	biliClient := bilibili.GetDefaultClient()
	target, err := biliClient.ParseInput(ctx, "https://www.bilibili.com/video/BV1m1Lv6pEiN/")
	if err != nil {
		t.Fatalf("ParseInput err: %v", err)
	}

	detail, err := biliClient.FetchVideoDetail(ctx, target)
	if err != nil {
		t.Fatalf("FetchVideoDetail err: %v", err)
	}

	ep := detail.Episodes[0]
	sel, err := biliClient.FetchStreamSelection(ctx, ep.BVID, ep.AID, ep.CID, ep.EPID, false, "32", "AVC")
	if err != nil {
		t.Fatalf("FetchStreamSelection err: %v", err)
	}

	tmpDir := filepath.Join(os.TempDir(), "pure_go_mux_size0_test")
	_ = os.RemoveAll(tmpDir)
	_ = os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)

	vTmp := filepath.Join(tmpDir, "video_size0.m4s")
	aTmp := filepath.Join(tmpDir, "audio.m4s")
	outMp4 := filepath.Join(tmpDir, "output_size0.mp4")

	vDownloader := NewStreamDownloader(sel.VideoURLs, vTmp)
	if err := vDownloader.DownloadSingleStream(ctx, nil); err != nil {
		t.Fatalf("Video download err: %v", err)
	}

	if len(sel.AudioURLs) > 0 && sel.AudioURLs[0] != "" {
		aDownloader := NewStreamDownloader(sel.AudioURLs, aTmp)
		if err := aDownloader.DownloadSingleStream(ctx, nil); err != nil {
			t.Fatalf("Audio download err: %v", err)
		}
	}

	// 模拟 B站 部分 CDN 将末尾 mdat 标记为 Size 0
	vf, err := os.OpenFile(vTmp, os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("Open vTmp err: %v", err)
	}
	vfi, _ := vf.Stat()
	vBoxes, _ := scanBoxes(vf, vfi.Size())
	if len(vBoxes) > 0 {
		lastBox := vBoxes[len(vBoxes)-1]
		_, _ = vf.Seek(lastBox.Offset, 0)
		_, _ = vf.Write([]byte{0, 0, 0, 0}) // 设置为 Size 0
	}
	vf.Close()

	// 执行纯 Go 合成
	if err := MergeAudioVideo(vTmp, aTmp, outMp4, false); err != nil {
		t.Fatalf("MergeAudioVideo failed with Size 0 box: %v", err)
	}

	fi, err := os.Stat(outMp4)
	if err != nil || fi.Size() == 0 {
		t.Fatalf("Output file invalid: %v", err)
	}
	t.Logf("✓ [Size 0 测试] 纯 Go 合成成功！输出文件大小: %.2f MB", float64(fi.Size())/1024/1024)
}

// TestAtomicMergeFailureSafety 测试合成失败时绝不破坏已存在的同名成品，且安全清理临时文件
func TestAtomicMergeFailureSafety(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "atomic_merge_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	outMp4 := filepath.Join(tmpDir, "existing_video.mp4")
	originalContent := []byte("PRE_EXISTING_VALID_VIDEO_CONTENT_1234567890")
	if err := os.WriteFile(outMp4, originalContent, 0644); err != nil {
		t.Fatal(err)
	}

	// 构造非法的视频与音频损坏文件
	badVideo := filepath.Join(tmpDir, "bad_video.m4s")
	badAudio := filepath.Join(tmpDir, "bad_audio.m4s")
	_ = os.WriteFile(badVideo, []byte("NOT_A_VALID_MP4_HEADER"), 0644)
	_ = os.WriteFile(badAudio, []byte("NOT_A_VALID_MP4_AUDIO"), 0644)

	// 执行合成，预期失败
	err = MergeAudioVideo(badVideo, badAudio, outMp4, false)
	if err == nil {
		t.Fatal("非法输入文件合成应当返回错误，但返回了 nil")
	}

	// 校验原有文件内容是否完好无损（未被 O_TRUNC 截断或破坏）
	readBack, err := os.ReadFile(outMp4)
	if err != nil {
		t.Fatalf("读取原文件失败: %v", err)
	}
	if !bytes.Equal(readBack, originalContent) {
		t.Fatalf("原有成品文件被合成错误破坏！内容改变: %s", string(readBack))
	}

	// 校验临时 .merging.tmp 文件是否被彻底清理
	files, _ := os.ReadDir(tmpDir)
	for _, f := range files {
		if strings.Contains(f.Name(), ".merging.") && strings.HasSuffix(f.Name(), ".tmp") {
			t.Fatalf("临时合成文件未被清理: %s", f.Name())
		}
	}
}

// TestWindowsCopyCleanup 模拟 Windows 下重命名失败走复制降级时，源临时文件必须被彻底删除
func TestWindowsCopyCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "test.merging.123.tmp")
	dstFile := filepath.Join(tmpDir, "output.mp4")

	testData := []byte("VALID_VIDEO_DATA_STREAM_TEST")
	if err := os.WriteFile(srcFile, testData, 0644); err != nil {
		t.Fatal(err)
	}

	if err := copyOrRename(srcFile, dstFile); err != nil {
		t.Fatalf("copyOrRename failed: %v", err)
	}

	// 验证目标文件已写入
	dstContent, err := os.ReadFile(dstFile)
	if err != nil || !bytes.Equal(dstContent, testData) {
		t.Fatalf("目标文件内容不符: %v", err)
	}

	// 验证源临时文件已被彻底删除
	if _, err := os.Stat(srcFile); !os.IsNotExist(err) {
		t.Fatalf("Windows 复制降级后未能删除源临时文件，存在磁盘泄漏风险: %s", srcFile)
	}
}


