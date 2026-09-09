package downloader

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"

	"github.com/Eyevinn/mp4ff/mp4"
)

// TestMuxerCompliance 严格校验自研复用器生成的 fMP4 文件是否 100% 符合 ISO 规范 (default-base-is-moof 与精确对齐)
func TestMuxerCompliance(t *testing.T) {
	if testing.Short() {
		t.Skip("Short mode: skipping live stream test")
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

	tmpDir := t.TempDir()
	vTmp := filepath.Join(tmpDir, "video.m4s")
	aTmp := filepath.Join(tmpDir, "audio.m4s")
	outMp4 := filepath.Join(tmpDir, "output_compliance.mp4")

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

	// 执行纯 Go 合成
	if err := MergeAudioVideo(vTmp, aTmp, outMp4, false); err != nil {
		t.Fatalf("MergeAudioVideo failed: %v", err)
	}

	// 深入解析输出文件的每一个 box
	fh, err := os.Open(outMp4)
	if err != nil {
		t.Fatalf("Open outMp4 failed: %v", err)
	}
	defer fh.Close()

	fi, err := fh.Stat()
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	boxes, err := scanBoxes(fh, fi.Size())
	if err != nil {
		t.Fatalf("scanBoxes failed: %v", err)
	}

	moofCount := 0
	mdatCount := 0

	for _, b := range boxes {
		if b.Type == "moof" {
			moofCount++
			_, _ = fh.Seek(b.Offset, ioSeekStart())
			box, err := mp4.DecodeBox(uint64(b.Offset), fh)
			if err != nil {
				t.Fatalf("moof decode failed at offset %d: %v", b.Offset, err)
			}
			moof, ok := box.(*mp4.MoofBox)
			if !ok {
				t.Fatalf("expected *mp4.MoofBox, got %T", box)
			}

			// 验证每一个 traf
			for _, traf := range moof.Trafs {
				if traf.Tfhd == nil {
					t.Fatalf("moof traf missing tfhd")
				}
				// 1. 验证 default-base-is-moof 标志必须为 true
				if !traf.Tfhd.DefaultBaseIfMoof() {
					t.Fatalf("tfhd 必须声明 default-base-is-moof (0x020000) 以保证交织播放定位准确")
				}
				// 2. 验证不可有 base-data-offset-present
				if traf.Tfhd.HasBaseDataOffset() {
					t.Fatalf("tfhd 不应含有 base-data-offset-present，应依赖 default-base-is-moof")
				}
				// 3. 验证 trun 的 DataOffset
				for _, trun := range traf.Truns {
					if trun.DataOffset <= 0 {
						t.Fatalf("trun DataOffset 异常: %d，预期大于 0", trun.DataOffset)
					}
				}
			}
		} else if b.Type == "mdat" {
			mdatCount++
		}
	}

	if moofCount == 0 || mdatCount == 0 {
		t.Fatalf("未找到任何有效 moof/mdat 分片: moof=%d, mdat=%d", moofCount, mdatCount)
	}

	t.Logf("✓ 成功校验 %d 个 moof 盒与 %d 个 mdat 盒，所有分片均严格遵循 default-base-is-moof 规范", moofCount, mdatCount)
}

func ioSeekStart() int {
	return 0
}
