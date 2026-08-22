package downloader

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Eyevinn/mp4ff/mp4"
)

// MergeAudioVideo 纯 Go 原生极速无损音视频流复用封装器 (100% 零外部依赖，毫秒级完成)
func MergeAudioVideo(videoPath, audioPath, outputPath string, deleteTemp bool) error {
	_ = os.MkdirAll(filepath.Dir(outputPath), 0755)

	// 如果无音频流或音频文件不存在，直接重命名/拷贝视频流
	if audioPath == "" || !fileExists(audioPath) {
		if err := copyOrRename(videoPath, outputPath); err != nil {
			return err
		}
		if deleteTemp {
			_ = os.Remove(videoPath)
		}
		return nil
	}

	// 1. 解码纯视频流 FMP4
	vFh, err := os.Open(videoPath)
	if err != nil {
		return fmt.Errorf("打开视频流文件失败: %w", err)
	}
	defer vFh.Close()

	vMp4, err := mp4.DecodeFile(vFh)
	if err != nil {
		return fmt.Errorf("解析视频 FMP4 结构失败: %w", err)
	}

	// 2. 解码纯音频流 FMP4
	aFh, err := os.Open(audioPath)
	if err != nil {
		return fmt.Errorf("打开音频流文件失败: %w", err)
	}
	defer aFh.Close()

	aMp4, err := mp4.DecodeFile(aFh)
	if err != nil {
		return fmt.Errorf("解析音频 FMP4 结构失败: %w", err)
	}

	// 3. 构建多轨道复合 FMP4 容器元数据
	if vMp4.Moov == nil || len(vMp4.Moov.Traks) == 0 {
		return fmt.Errorf("视频流缺少 moov 元数据")
	}
	if aMp4.Moov == nil || len(aMp4.Moov.Traks) == 0 {
		return fmt.Errorf("音频流缺少 moov 元数据")
	}

	// 将音频轨道 Track ID 调整为 2 (避免与视频轨 ID 1 冲突)
	const audioTrackID = uint32(2)
	aTrak := aMp4.Moov.Traks[0]
	aTrak.Tkhd.TrackID = audioTrackID

	if aMp4.Moov.Mvex != nil && len(aMp4.Moov.Mvex.Trexs) > 0 {
		aMp4.Moov.Mvex.Trexs[0].TrackID = audioTrackID
		if vMp4.Moov.Mvex != nil {
			vMp4.Moov.Mvex.AddChild(aMp4.Moov.Mvex.Trexs[0])
		}
	}

	// 将音频 trak 加入到视频 moov 中
	vMp4.Moov.AddChild(aTrak)
	vMp4.Moov.Mvhd.NextTrackID = 3

	// 更新所有音频分片 moof 中的 Track ID 为 2
	for _, child := range aMp4.Children {
		if moof, ok := child.(*mp4.MoofBox); ok {
			for _, traf := range moof.Trafs {
				traf.Tfhd.TrackID = audioTrackID
			}
		}
	}

	// 4. 创建最终输出 MP4
	outFh, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("创建最终合成文件失败: %w", err)
	}
	defer outFh.Close()

	// 写入 ftyp
	if vMp4.Ftyp != nil {
		if err := vMp4.Ftyp.Encode(outFh); err != nil {
			return fmt.Errorf("写入 ftyp 失败: %w", err)
		}
	}

	// 写入包含双轨道的复合 moov
	if err := vMp4.Moov.Encode(outFh); err != nil {
		return fmt.Errorf("写入 moov 失败: %w", err)
	}

	// 5. 依次写入视频与音频的媒体分片数据 (moof + mdat)
	for _, child := range vMp4.Children {
		switch box := child.(type) {
		case *mp4.MoofBox:
			if err := box.Encode(outFh); err != nil {
				return fmt.Errorf("写入视频 moof 失败: %w", err)
			}
		case *mp4.MdatBox:
			if err := box.Encode(outFh); err != nil {
				return fmt.Errorf("写入视频 mdat 失败: %w", err)
			}
		}
	}

	for _, child := range aMp4.Children {
		switch box := child.(type) {
		case *mp4.MoofBox:
			if err := box.Encode(outFh); err != nil {
				return fmt.Errorf("写入音频 moof 失败: %w", err)
			}
		case *mp4.MdatBox:
			if err := box.Encode(outFh); err != nil {
				return fmt.Errorf("写入音频 mdat 失败: %w", err)
			}
		}
	}

	outFh.Close()

	// 6. 清理临时分块文件
	if deleteTemp {
		_ = os.Remove(videoPath)
		if audioPath != "" {
			_ = os.Remove(audioPath)
		}
	}

	return nil
}

func copyOrRename(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()
	d, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer d.Close()
	_, err = io.Copy(d, s)
	return err
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
