package downloader

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

			vDownloader := NewStreamDownloader(sel.VideoURL, vTmp)
			if err := vDownloader.DownloadSingleStream(ctx, nil); err != nil {
				t.Fatalf("[%s] Video download err: %v", codec, err)
			}

			if sel.AudioURL != "" {
				aDownloader := NewStreamDownloader(sel.AudioURL, aTmp)
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
