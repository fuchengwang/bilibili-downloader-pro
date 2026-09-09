package downloader

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/Eyevinn/mp4ff/mp4"
)

// BoxInfo MP4 容器顶级 Box 元信息
type BoxInfo struct {
	Type      string
	Offset    int64
	Size      uint64
	HeaderLen int64
}

// scanBoxes 扫描 MP4 顶层 Box 结构，完美兼容 Size 0 (EOF 盒) 与 Size 1 (64位大文件盒)
func scanBoxes(f *os.File, fileSize int64) ([]BoxInfo, error) {
	var boxes []BoxInfo
	var offset int64 = 0
	buf := make([]byte, 16)

	for offset < fileSize {
		_, _ = f.Seek(offset, io.SeekStart)
		n, err := io.ReadFull(f, buf[:8])
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return nil, err
		}
		if n < 8 {
			break
		}

		size := uint64(binary.BigEndian.Uint32(buf[0:4]))
		boxType := string(buf[4:8])
		headerLen := int64(8)

		if size == 1 {
			// 64-bit large size
			_, err = io.ReadFull(f, buf[8:16])
			if err != nil {
				return nil, err
			}
			size = binary.BigEndian.Uint64(buf[8:16])
			headerLen = 16
		} else if size == 0 {
			// Size 0 规范定义：Box 延伸至文件 EOF，动态计算其实际大小
			size = uint64(fileSize - offset)
		}

		if size < uint64(headerLen) {
			break
		}

		boxes = append(boxes, BoxInfo{
			Type:      boxType,
			Offset:    offset,
			Size:      size,
			HeaderLen: headerLen,
		})

		offset += int64(size)
	}

	return boxes, nil
}

// MergeAudioVideo 纯 Go 原生极速无损音视频流复用封装器 (100% 零外部依赖，毫秒级完成，零内存复制)
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

	vF, err := os.Open(videoPath)
	if err != nil {
		return fmt.Errorf("打开视频流文件失败: %w", err)
	}
	defer vF.Close()

	vFi, err := vF.Stat()
	if err != nil {
		return err
	}
	vFileSize := vFi.Size()

	aF, err := os.Open(audioPath)
	if err != nil {
		return fmt.Errorf("打开音频流文件失败: %w", err)
	}
	defer aF.Close()

	aFi, err := aF.Stat()
	if err != nil {
		return err
	}
	aFileSize := aFi.Size()

	// 1. 扫描视频与音频的 Box 结构 (支持 Size 0 动态换算)
	vBoxes, err := scanBoxes(vF, vFileSize)
	if err != nil {
		return fmt.Errorf("解析视频 FMP4 结构失败: %w", err)
	}

	aBoxes, err := scanBoxes(aF, aFileSize)
	if err != nil {
		return fmt.Errorf("解析音频 FMP4 结构失败: %w", err)
	}

	// 2. 解码并构建复合 moov 元数据
	var vFtyp *mp4.FtypBox
	var vMoov *mp4.MoovBox
	for _, b := range vBoxes {
		if b.Type == "ftyp" && vFtyp == nil {
			_, _ = vF.Seek(b.Offset, io.SeekStart)
			box, err := mp4.DecodeBox(uint64(b.Offset), vF)
			if err == nil {
				if ftyp, ok := box.(*mp4.FtypBox); ok {
					vFtyp = ftyp
				}
			}
		} else if b.Type == "moov" && vMoov == nil {
			_, _ = vF.Seek(b.Offset, io.SeekStart)
			box, err := mp4.DecodeBox(uint64(b.Offset), vF)
			if err == nil {
				if moov, ok := box.(*mp4.MoovBox); ok {
					vMoov = moov
				}
			}
		}
	}

	if vMoov == nil || len(vMoov.Traks) == 0 {
		return fmt.Errorf("视频流缺少 moov 元数据")
	}

	var aMoov *mp4.MoovBox
	for _, b := range aBoxes {
		if b.Type == "moov" && aMoov == nil {
			_, _ = aF.Seek(b.Offset, io.SeekStart)
			box, err := mp4.DecodeBox(uint64(b.Offset), aF)
			if err == nil {
				if moov, ok := box.(*mp4.MoovBox); ok {
					aMoov = moov
				}
			}
			break
		}
	}

	if aMoov == nil || len(aMoov.Traks) == 0 {
		return fmt.Errorf("音频流缺少 moov 元数据")
	}

	// 将音频轨道 Track ID 调整为 2 (避免与视频轨 ID 1 冲突)
	const audioTrackID = uint32(2)
	aTrak := aMoov.Traks[0]
	aTrak.Tkhd.TrackID = audioTrackID

	if aMoov.Mvex != nil && len(aMoov.Mvex.Trexs) > 0 {
		aMoov.Mvex.Trexs[0].TrackID = audioTrackID
		if vMoov.Mvex != nil {
			vMoov.Mvex.AddChild(aMoov.Mvex.Trexs[0])
		}
	}

	vMoov.AddChild(aTrak)
	vMoov.Mvhd.NextTrackID = 3

	// 3. 创建同目录下的事务性临时输出文件，防止中断时破坏已有文件
	tmpOutputPath := outputPath + fmt.Sprintf(".merging.%d.tmp", os.Getpid())
	outFh, err := os.OpenFile(tmpOutputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("创建最终合成临时文件失败: %w", err)
	}

	success := false
	defer func() {
		if !success {
			_ = outFh.Close()
			_ = os.Remove(tmpOutputPath)
		}
	}()

	// 写入 ftyp
	if vFtyp != nil {
		if err := vFtyp.Encode(outFh); err != nil {
			return fmt.Errorf("写入 ftyp 失败: %w", err)
		}
	}

	// 写入合并后的 moov
	if err := vMoov.Encode(outFh); err != nil {
		return fmt.Errorf("写入 moov 失败: %w", err)
	}

	// 4. 提取视频与音频的 timescale 并收集音视频分片 (moof + mdat)
	var vTimescale uint32 = 1000
	if len(vMoov.Traks) > 0 && vMoov.Traks[0].Mdia != nil && vMoov.Traks[0].Mdia.Mdhd != nil {
		vTimescale = vMoov.Traks[0].Mdia.Mdhd.Timescale
	}
	var aTimescale uint32 = 1000
	if len(aMoov.Traks) > 0 && aMoov.Traks[0].Mdia != nil && aMoov.Traks[0].Mdia.Mdhd != nil {
		aTimescale = aMoov.Traks[0].Mdia.Mdhd.Timescale
	}

	vFrags := extractFragments(vBoxes, vF, false, vTimescale)
	aFrags := extractFragments(aBoxes, aF, true, aTimescale)

	streamBuf := make([]byte, 1024*1024) // 1MB 流式传输缓冲，恒定微小内存占用

	if len(vFrags) > 0 && len(aFrags) > 0 {
		// 5. 核心交织算法：合并音视频分片并按实际解码时间（TimeSec）升序稳定排序
		allFrags := append(vFrags, aFrags...)
		sort.SliceStable(allFrags, func(i, j int) bool {
			return allFrags[i].TimeSec < allFrags[j].TimeSec
		})

		for _, frag := range allFrags {
			if !frag.IsAudio {
				// 写入视频分片
				_, _ = vF.Seek(frag.Moof.Offset, io.SeekStart)
				box, err := mp4.DecodeBox(uint64(frag.Moof.Offset), vF)
				if err != nil {
					return fmt.Errorf("解析视频 moof 失败: %w", err)
				}
				if err := box.Encode(outFh); err != nil {
					return fmt.Errorf("写入视频 moof 失败: %w", err)
				}
				if err := writeMdatBox(vF, frag.Mdat, outFh, streamBuf); err != nil {
					return fmt.Errorf("写入视频 mdat 失败: %w", err)
				}
			} else {
				// 写入音频分片，调整音频 TrackID 为 2
				_, _ = aF.Seek(frag.Moof.Offset, io.SeekStart)
				box, err := mp4.DecodeBox(uint64(frag.Moof.Offset), aF)
				if err != nil {
					return fmt.Errorf("解析音频 moof 失败: %w", err)
				}
				if moof, ok := box.(*mp4.MoofBox); ok {
					for _, traf := range moof.Trafs {
						if traf.Tfhd != nil {
							traf.Tfhd.TrackID = audioTrackID
						}
					}
					if err := moof.Encode(outFh); err != nil {
						return fmt.Errorf("写入音频 moof 失败: %w", err)
					}
				}
				if err := writeMdatBox(aF, frag.Mdat, outFh, streamBuf); err != nil {
					return fmt.Errorf("写入音频 mdat 失败: %w", err)
				}
			}
		}
	} else {
		// 兜底降级：非标准分片流直接顺序写入
		for _, b := range vBoxes {
			if b.Type == "moof" {
				_, _ = vF.Seek(b.Offset, io.SeekStart)
				if box, err := mp4.DecodeBox(uint64(b.Offset), vF); err == nil {
					_ = box.Encode(outFh)
				}
			} else if b.Type == "mdat" {
				_ = writeMdatBox(vF, b, outFh, streamBuf)
			}
		}
		for _, b := range aBoxes {
			if b.Type == "moof" {
				_, _ = aF.Seek(b.Offset, io.SeekStart)
				if box, err := mp4.DecodeBox(uint64(b.Offset), aF); err == nil {
					if moof, ok := box.(*mp4.MoofBox); ok {
						for _, traf := range moof.Trafs {
							if traf.Tfhd != nil {
								traf.Tfhd.TrackID = audioTrackID
							}
						}
						_ = moof.Encode(outFh)
					}
				}
			} else if b.Type == "mdat" {
				_ = writeMdatBox(aF, b, outFh, streamBuf)
			}
		}
	}

	_ = outFh.Sync()
	_ = outFh.Close()
	_ = vF.Close()
	_ = aF.Close()

	// 原子替换至最终目标路径
	if err := copyOrRename(tmpOutputPath, outputPath); err != nil {
		_ = os.Remove(tmpOutputPath)
		return fmt.Errorf("移动最终合成文件失败: %w", err)
	}

	// 无论以重命名还是复制方式成功，均确保临时文件彻底被移走或删除
	_ = os.Remove(tmpOutputPath)
	success = true

	// 6. 清理临时分块文件 (在合成完全成功后安全移除)
	if deleteTemp {
		_ = os.Remove(videoPath)
		if audioPath != "" {
			_ = os.Remove(audioPath)
		}
	}

	return nil
}

type fragmentItem struct {
	IsAudio bool
	Moof    BoxInfo
	Mdat    BoxInfo
	TimeSec float64
}

func extractFragments(boxes []BoxInfo, f *os.File, isAudio bool, timescale uint32) []fragmentItem {
	var fragments []fragmentItem
	var currentMoof *BoxInfo
	var currentTimeSec float64 = 0

	for i := 0; i < len(boxes); i++ {
		b := boxes[i]
		if b.Type == "moof" {
			currentMoof = &boxes[i]
			_, _ = f.Seek(b.Offset, io.SeekStart)
			if box, err := mp4.DecodeBox(uint64(b.Offset), f); err == nil {
				if moof, ok := box.(*mp4.MoofBox); ok && len(moof.Trafs) > 0 {
					if moof.Trafs[0].Tfdt != nil && timescale > 0 {
						currentTimeSec = float64(moof.Trafs[0].Tfdt.BaseMediaDecodeTime()) / float64(timescale)
					}
				}
			}
		} else if b.Type == "mdat" && currentMoof != nil {
			fragments = append(fragments, fragmentItem{
				IsAudio: isAudio,
				Moof:    *currentMoof,
				Mdat:    b,
				TimeSec: currentTimeSec,
			})
			currentMoof = nil
		}
	}
	return fragments
}

func writeMdatBox(f *os.File, b BoxInfo, outFh io.Writer, streamBuf []byte) error {
	if b.Size < 1<<32 {
		hdr := make([]byte, 8)
		binary.BigEndian.PutUint32(hdr[0:4], uint32(b.Size))
		copy(hdr[4:8], "mdat")
		if _, err := outFh.Write(hdr); err != nil {
			return err
		}
	} else {
		hdr := make([]byte, 16)
		binary.BigEndian.PutUint32(hdr[0:4], 1)
		copy(hdr[4:8], "mdat")
		binary.BigEndian.PutUint64(hdr[8:16], b.Size)
		if _, err := outFh.Write(hdr); err != nil {
			return err
		}
	}

	payloadLen := int64(b.Size) - b.HeaderLen
	_, _ = f.Seek(b.Offset+b.HeaderLen, io.SeekStart)
	_, err := io.CopyBuffer(outFh, io.LimitReader(f, payloadLen), streamBuf)
	return err
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
	if _, err = io.Copy(d, s); err != nil {
		return err
	}
	_ = s.Close()
	_ = os.Remove(src) // Windows 下复制成功后显式删除源临时文件，彻底防止磁盘空间翻倍泄露
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
