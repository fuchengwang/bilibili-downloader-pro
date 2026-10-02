package downloader

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"bilibili_downloader/pkg/utils"

	"github.com/Eyevinn/mp4ff/mp4"
)

// BoxInfo MP4 容器顶级 Box 元信息
type BoxInfo struct {
	Type      string
	Offset    int64
	Size      uint64
	HeaderLen int64
}

// scanBoxes 扫描 MP4 顶层 Box 结构，完美兼容 Size 0 (EOF 盒) 与 Size 1 (64位大文件盒)，具备严格的整数溢出与死循环安全防御
func scanBoxes(f *os.File, fileSize int64) ([]BoxInfo, error) {
	if fileSize < 0 {
		return nil, fmt.Errorf("文件大小无效: %d", fileSize)
	}

	var boxes []BoxInfo
	var offset int64 = 0
	buf := make([]byte, 16)

	for offset < fileSize {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return boxes, fmt.Errorf("定位 Box %d 失败: %w", offset, err)
		}
		n, err := io.ReadFull(f, buf[:8])
		if err != nil {
			return boxes, fmt.Errorf("读取 Box 头失败 (offset=%d, read=%d/%d): %w", offset, n, len(buf[:8]), err)
		}

		size := uint64(binary.BigEndian.Uint32(buf[0:4]))
		boxType := string(buf[4:8])
		headerLen := int64(8)

		if size == 1 {
			// 64-bit large size
			_, err = io.ReadFull(f, buf[8:16])
			if err != nil {
				return boxes, fmt.Errorf("读取 Box largesize 失败 (offset=%d): %w", offset, err)
			}
			size = binary.BigEndian.Uint64(buf[8:16])
			headerLen = 16
		} else if size == 0 {
			// Size 0 规范定义：Box 延伸至文件 EOF，动态计算其实际大小
			size = uint64(fileSize - offset)
		}

		if size < uint64(headerLen) {
			return boxes, fmt.Errorf("Box 大小小于头长度 (offset=%d, size=%d, header=%d)", offset, size, headerLen)
		}

		// 核心溢出与边界保护：
		// 1. int64(size) 不能为负数 (防止 uint64 溢出反转)
		// 2. size 不能超过文件剩余可用字节数 (fileSize - offset)
		// 3. offset + int64(size) 不能上溢 (必须严格大于原 offset)
		int64Size := int64(size)
		if int64Size < 0 || size > uint64(fileSize-offset) || offset+int64Size <= offset {
			return boxes, fmt.Errorf("Box 超出文件边界或发生整数溢出 (offset=%d, size=%d, fileSize=%d)", offset, size, fileSize)
		}

		boxes = append(boxes, BoxInfo{
			Type:      boxType,
			Offset:    offset,
			Size:      size,
			HeaderLen: headerLen,
		})

		offset += int64Size
	}

	return boxes, nil
}

// MergeAudioVideo 纯 Go 原生音视频复用封装器 (零 FFmpeg 外部依赖，原生支持 fMP4 / MP4 / 裸 FLAC)
func MergeAudioVideo(videoPath, audioPath, outputPath string, deleteTemp bool) error {
	return MergeAudioVideoContext(context.Background(), videoPath, audioPath, outputPath, deleteTemp)
}

// MergeAudioVideoContext 支持被任务取消/暂停时尽快中止合并，并保证临时输出被清理。
func MergeAudioVideoContext(ctx context.Context, videoPath, audioPath, outputPath string, deleteTemp bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(outputPath) == "" {
		return fmt.Errorf("输出文件路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	// Only an explicitly absent audio stream may produce a video-only output.
	if audioPath != "" {
		if info, err := os.Stat(audioPath); err != nil || info.IsDir() {
			return fmt.Errorf("音频流文件不可用: %s", audioPath)
		}
	}
	if audioPath == "" {
		var err error
		if deleteTemp {
			err = copyOrRenameContext(ctx, videoPath, outputPath)
		} else {
			err = copyPreservingSourceContext(ctx, videoPath, outputPath)
		}
		if err != nil {
			return err
		}
		return nil
	}

	actualAudioPath := audioPath
	var tmpFmp4 string

	// 检测是否为原生裸 FLAC 音频流 (以 "fLaC" 签名开头)
	if IsRawFlac(audioPath) {
		tmpFmp4 = filepath.Join(filepath.Dir(outputPath), fmt.Sprintf(".tmp_flac_%d_%x.m4s", os.Getpid(), time.Now().UnixNano()))
		_ = os.Remove(tmpFmp4)
		if err := PackageRawFlacToFMP4Context(ctx, audioPath, tmpFmp4); err != nil {
			_ = os.Remove(tmpFmp4)
			return fmt.Errorf("裸 FLAC 音频封装为 fMP4 失败: %w", err)
		}
		actualAudioPath = tmpFmp4
	}

	// 直接调用纯 Go 原生复用封装
	err := mergeWithPureGoContext(ctx, videoPath, actualAudioPath, outputPath, deleteTemp)

	if tmpFmp4 != "" {
		_ = os.Remove(tmpFmp4)
		if err == nil && deleteTemp {
			_ = os.Remove(audioPath)
		}
	}

	return err
}

func mergeWithPureGoContext(ctx context.Context, videoPath, audioPath, outputPath string, deleteTemp bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(outputPath) == "" {
		return fmt.Errorf("输出文件路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	if audioPath != "" {
		if info, err := os.Stat(audioPath); err != nil || info.IsDir() {
			return fmt.Errorf("音频流文件不可用: %s", audioPath)
		}
	}
	if audioPath == "" {
		var err error
		if deleteTemp {
			err = copyOrRenameContext(ctx, videoPath, outputPath)
		} else {
			err = copyPreservingSourceContext(ctx, videoPath, outputPath)
		}
		if err != nil {
			return err
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
	if err := ctx.Err(); err != nil {
		return err
	}
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
			if _, err := vF.Seek(b.Offset, io.SeekStart); err != nil {
				return fmt.Errorf("定位视频 ftyp 失败: %w", err)
			}
			box, err := mp4.DecodeBox(uint64(b.Offset), vF)
			if err != nil {
				return fmt.Errorf("解析视频 ftyp 失败: %w", err)
			}
			ftyp, ok := box.(*mp4.FtypBox)
			if !ok {
				return fmt.Errorf("视频 ftyp Box 类型异常: %T", box)
			}
			vFtyp = ftyp
		} else if b.Type == "moov" && vMoov == nil {
			if _, err := vF.Seek(b.Offset, io.SeekStart); err != nil {
				return fmt.Errorf("定位视频 moov 失败: %w", err)
			}
			box, err := mp4.DecodeBox(uint64(b.Offset), vF)
			if err != nil {
				return fmt.Errorf("解析视频 moov 失败: %w", err)
			}
			moov, ok := box.(*mp4.MoovBox)
			if !ok {
				return fmt.Errorf("视频 moov Box 类型异常: %T", box)
			}
			vMoov = moov
		}
	}

	if vMoov == nil || len(vMoov.Traks) == 0 {
		return fmt.Errorf("视频流缺少 moov 元数据")
	}

	var aMoov *mp4.MoovBox
	for _, b := range aBoxes {
		if b.Type == "moov" && aMoov == nil {
			if _, err := aF.Seek(b.Offset, io.SeekStart); err != nil {
				return fmt.Errorf("定位音频 moov 失败: %w", err)
			}
			box, err := mp4.DecodeBox(uint64(b.Offset), aF)
			if err != nil {
				return fmt.Errorf("解析音频 moov 失败: %w", err)
			}
			moov, ok := box.(*mp4.MoovBox)
			if !ok {
				return fmt.Errorf("音频 moov Box 类型异常: %T", box)
			}
			aMoov = moov
			break
		}
	}

	if aMoov == nil || len(aMoov.Traks) == 0 {
		return fmt.Errorf("音频流缺少 moov 元数据")
	}
	if vMoov.Mvhd == nil {
		return fmt.Errorf("视频流缺少 mvhd 元数据")
	}
	for i, trak := range vMoov.Traks {
		if trak == nil || trak.Tkhd == nil {
			return fmt.Errorf("视频轨道 %d 缺少 tkhd 元数据", i)
		}
	}
	aTrak := aMoov.Traks[0]
	if aTrak == nil || aTrak.Tkhd == nil {
		return fmt.Errorf("音频轨道缺少 tkhd 元数据")
	}

	// 动态计算音频轨道 Track ID (取视频 moov 中最大 Track ID + 1)，
	// 避免与包含多条视频/字幕轨的源文件发生 ID 冲突
	maxTrackID := uint32(0)
	for _, trak := range vMoov.Traks {
		if trak.Tkhd.TrackID > maxTrackID {
			maxTrackID = trak.Tkhd.TrackID
		}
	}
	if maxTrackID >= ^uint32(0)-1 {
		return fmt.Errorf("视频 Track ID 无法分配新的音频轨道")
	}
	audioTrackID := maxTrackID + 1
	aTrak.Tkhd.TrackID = audioTrackID

	if aMoov.Mvex != nil {
		for _, trex := range aMoov.Mvex.Trexs {
			if trex == nil {
				continue
			}
			trex.TrackID = audioTrackID
			if vMoov.Mvex != nil {
				vMoov.Mvex.AddChild(trex)
			}
		}
	}

	vMoov.AddChild(aTrak)
	vMoov.Mvhd.NextTrackID = audioTrackID + 1

	// 3. 创建同目录下的事务性临时输出文件，防止中断时破坏已有文件 (采用短文件名，彻底避免 Windows MAX_PATH 260 截断错误)
	tmpOutputPath := filepath.Join(filepath.Dir(outputPath), fmt.Sprintf(".tmp_mrg_%d_%x.tmp", os.Getpid(), time.Now().UnixNano()))
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

	// 4. 提取视频与音频的 timescale 并收集音视频分片 (moof + mdat)
	var vTimescale uint32 = 1000
	if len(vMoov.Traks) > 0 && vMoov.Traks[0].Mdia != nil && vMoov.Traks[0].Mdia.Mdhd != nil {
		vTimescale = vMoov.Traks[0].Mdia.Mdhd.Timescale
	}
	var aTimescale uint32 = 1000
	if len(aMoov.Traks) > 0 && aMoov.Traks[0].Mdia != nil && aMoov.Traks[0].Mdia.Mdhd != nil {
		aTimescale = aMoov.Traks[0].Mdia.Mdhd.Timescale
	}

	vFrags, err := extractFragments(vBoxes, vF, false, vTimescale)
	if err != nil {
		return fmt.Errorf("解析视频分片失败: %w", err)
	}
	aFrags, err := extractFragments(aBoxes, aF, true, aTimescale)
	if err != nil {
		return fmt.Errorf("解析音频分片失败: %w", err)
	}

	// 若为传统非分片 MP4 (无 moof 盒)，在写入 moov 前对 stco / co64 的 Chunk Offset 进行绝对偏移校准
	if len(vFrags) == 0 && len(aFrags) == 0 {
		// Always use 64-bit offsets for classic MP4 before measuring moov. This avoids
		// header growth after relocation and prevents audio offsets wrapping past 4 GB.
		for _, trak := range vMoov.Traks {
			promoteChunkOffsets(trak)
		}
		newHeaderSize := int64(vMoov.Size())
		if vFtyp != nil {
			newHeaderSize += int64(vFtyp.Size())
		}
		var videoMediaSize int64
		for _, box := range vBoxes {
			if box.Type == "mdat" {
				size := getMdatHeaderLen(box) + int64(box.Size) - box.HeaderLen
				if videoMediaSize > math.MaxInt64-size {
					return fmt.Errorf("视频数据大小溢出")
				}
				videoMediaSize += size
			}
		}
		if videoMediaSize > math.MaxInt64-newHeaderSize {
			return fmt.Errorf("合并文件偏移溢出")
		}
		for _, trak := range vMoov.Traks {
			if trak.Tkhd.TrackID == audioTrackID {
				continue
			}
			if err := relocateChunkOffsets(trak, vBoxes, newHeaderSize); err != nil {
				return fmt.Errorf("校准视频偏移失败: %w", err)
			}
		}
		if err := relocateChunkOffsets(aTrak, aBoxes, newHeaderSize+videoMediaSize); err != nil {
			return fmt.Errorf("校准音频偏移失败: %w", err)
		}

	}

	// 写入 ftyp
	if vFtyp != nil {
		if err := vFtyp.Encode(outFh); err != nil {
			return fmt.Errorf("写入 ftyp 失败: %w", err)
		}
	}

	// 写入合并并校准后的 moov
	if err := vMoov.Encode(outFh); err != nil {
		return fmt.Errorf("写入 moov 失败: %w", err)
	}

	streamBuf := make([]byte, 1024*1024) // 1MB 流式传输缓冲，恒定微小内存占用

	if len(vFrags) > 0 && len(aFrags) > 0 {
		// 5. 核心交织算法：合并音视频分片并按实际解码时间（TimeSec）升序稳定排序
		allFrags := append(vFrags, aFrags...)
		sort.SliceStable(allFrags, func(i, j int) bool {
			return allFrags[i].TimeSec < allFrags[j].TimeSec
		})

		var seqNum uint32 = 1
		for _, frag := range allFrags {
			if err := ctx.Err(); err != nil {
				return err
			}
			srcFile := vF
			var targetTrackID uint32 = 0
			if frag.IsAudio {
				srcFile = aF
				targetTrackID = audioTrackID
			}

			if _, err := srcFile.Seek(frag.Moof.Offset, io.SeekStart); err != nil {
				return fmt.Errorf("定位 moof 失败: %w", err)
			}
			box, err := mp4.DecodeBox(uint64(frag.Moof.Offset), srcFile)
			if err != nil {
				return fmt.Errorf("解析 moof 失败 (isAudio=%v): %w", frag.IsAudio, err)
			}

			if moof, ok := box.(*mp4.MoofBox); ok {
				// Resolve defaults using the final track ID; source audio/video IDs can both be 1.
				if err := resolveFragmentSampleDefaults(moof, vMoov.Mvex, targetTrackID); err != nil {
					return err
				}
				// 核心协议合规性修复：强制设置 default-base-is-moof，并校准 trun DataOffset 以及 Mfhd 序列号
				if err := adjustMoofBox(moof, targetTrackID, getMdatHeaderLen(frag.Mdat), seqNum); err != nil {
					return fmt.Errorf("校准 moof 失败 (isAudio=%v): %w", frag.IsAudio, err)
				}
				seqNum++
				if err := moof.Encode(outFh); err != nil {
					return fmt.Errorf("写入 moof 失败: %w", err)
				}
			} else {
				if err := box.Encode(outFh); err != nil {
					return fmt.Errorf("写入非标准 moof 盒失败: %w", err)
				}
			}

			if err := writeMdatBoxContext(ctx, srcFile, frag.Mdat, outFh, streamBuf); err != nil {
				return fmt.Errorf("写入 mdat 失败: %w", err)
			}
		}
	} else if len(vFrags) == 0 && len(aFrags) == 0 {
		// 传统非分片流直接顺序写入；任一侧只有半套分片时必须失败，
		// 禁止把缺失轨道当作“降级成功”。
		var seqNum uint32 = 1
		for _, b := range vBoxes {
			if err := ctx.Err(); err != nil {
				return err
			}
			if b.Type == "moof" {
				if _, err := vF.Seek(b.Offset, io.SeekStart); err != nil {
					return fmt.Errorf("定位视频 moof 失败: %w", err)
				}
				box, err := mp4.DecodeBox(uint64(b.Offset), vF)
				if err != nil {
					return fmt.Errorf("解析视频 moof 失败: %w", err)
				}
				moof, ok := box.(*mp4.MoofBox)
				if !ok {
					return fmt.Errorf("视频 moof 类型异常: %T", box)
				}
				if moof.Mfhd != nil {
					moof.Mfhd.SequenceNumber = seqNum
				}
				seqNum++
				if err := moof.Encode(outFh); err != nil {
					return fmt.Errorf("写入视频 moof 失败: %w", err)
				}
			} else if b.Type == "mdat" {
				if err := writeMdatBoxContext(ctx, vF, b, outFh, streamBuf); err != nil {
					return fmt.Errorf("写入视频 mdat 失败: %w", err)
				}
			}
		}
		for _, b := range aBoxes {
			if err := ctx.Err(); err != nil {
				return err
			}
			if b.Type == "moof" {
				if _, err := aF.Seek(b.Offset, io.SeekStart); err != nil {
					return fmt.Errorf("定位音频 moof 失败: %w", err)
				}
				box, err := mp4.DecodeBox(uint64(b.Offset), aF)
				if err != nil {
					return fmt.Errorf("解析音频 moof 失败: %w", err)
				}
				moof, ok := box.(*mp4.MoofBox)
				if !ok {
					return fmt.Errorf("音频 moof 类型异常: %T", box)
				}
				for _, traf := range moof.Trafs {
					if traf.Tfhd != nil {
						traf.Tfhd.TrackID = audioTrackID
					}
				}
				if moof.Mfhd != nil {
					moof.Mfhd.SequenceNumber = seqNum
				}
				seqNum++
				if err := moof.Encode(outFh); err != nil {
					return fmt.Errorf("写入音频 moof 失败: %w", err)
				}
			} else if b.Type == "mdat" {
				if err := writeMdatBoxContext(ctx, aF, b, outFh, streamBuf); err != nil {
					return fmt.Errorf("写入音频 mdat 失败: %w", err)
				}
			}
		}
	} else {
		return fmt.Errorf("音视频分片结构不完整 (video=%d, audio=%d)", len(vFrags), len(aFrags))
	}

	if err := outFh.Sync(); err != nil {
		return fmt.Errorf("同步合成临时文件失败: %w", err)
	}
	if err := outFh.Close(); err != nil {
		return fmt.Errorf("关闭合成临时文件失败: %w", err)
	}

	// 原子替换至最终目标路径
	if err := copyOrRenameContext(ctx, tmpOutputPath, outputPath); err != nil {
		_ = os.Remove(tmpOutputPath)
		return fmt.Errorf("移动最终合成文件失败: %w", err)
	}

	// 无论以重命名还是复制方式成功，均确保临时文件彻底被移走或删除
	_ = os.Remove(tmpOutputPath)
	success = true

	// 6. 清理临时分块文件 (在合成完全成功后安全移除，采用指数退避重试防御 Windows 句柄锁)
	if deleteTemp {
		utils.SafeRemoveWithRetry(videoPath)
		if audioPath != "" {
			utils.SafeRemoveWithRetry(audioPath)
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

func extractFragments(boxes []BoxInfo, f *os.File, isAudio bool, timescale uint32) ([]fragmentItem, error) {
	var fragments []fragmentItem
	var currentMoof *BoxInfo
	var currentTimeSec float64 = 0

	for i := 0; i < len(boxes); i++ {
		b := boxes[i]
		if b.Type == "moof" {
			if currentMoof != nil {
				return nil, fmt.Errorf("moof 缺少对应的 mdat (offset=%d)", currentMoof.Offset)
			}
			currentMoof = &boxes[i]
			if _, err := f.Seek(b.Offset, io.SeekStart); err != nil {
				return nil, fmt.Errorf("定位 moof 失败: %w", err)
			}
			box, err := mp4.DecodeBox(uint64(b.Offset), f)
			if err != nil {
				return nil, fmt.Errorf("解码 moof 失败: %w", err)
			}
			moof, ok := box.(*mp4.MoofBox)
			if !ok || len(moof.Trafs) == 0 {
				return nil, fmt.Errorf("moof 缺少有效 traf (offset=%d)", b.Offset)
			}
			if moof.Trafs[0] == nil {
				return nil, fmt.Errorf("moof 的首个 traf 为空 (offset=%d)", b.Offset)
			}
			currentTimeSec = 0
			if moof.Trafs[0].Tfdt != nil && timescale > 0 {
				currentTimeSec = float64(moof.Trafs[0].Tfdt.BaseMediaDecodeTime()) / float64(timescale)
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
	if currentMoof != nil {
		return nil, fmt.Errorf("moof 缺少对应的 mdat (offset=%d)", currentMoof.Offset)
	}
	return fragments, nil
}

// getMdatHeaderLen 准确计算重写后的 mdat Box 头长度 (8 或 16 字节)，与 writeMdatBox 严格一致
func getMdatHeaderLen(b BoxInfo) int64 {
	payloadLen := int64(b.Size) - b.HeaderLen
	if payloadLen+8 < 1<<32 {
		return 8
	}
	return 16
}

func writeMdatBox(f *os.File, b BoxInfo, outFh io.Writer, streamBuf []byte) error {
	return writeMdatBoxContext(context.Background(), f, b, outFh, streamBuf)
}

func writeMdatBoxContext(ctx context.Context, f *os.File, b BoxInfo, outFh io.Writer, streamBuf []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.Type != "mdat" || b.Size < uint64(b.HeaderLen) || b.HeaderLen < 8 {
		return fmt.Errorf("mdat Box 元数据无效: %+v", b)
	}
	payloadLen := int64(b.Size) - b.HeaderLen
	if payloadLen < 0 {
		return fmt.Errorf("mdat payload 大小无效: %d", payloadLen)
	}
	if len(streamBuf) == 0 {
		streamBuf = make([]byte, 128*1024)
	}
	writeAll := func(data []byte) error {
		for len(data) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, err := outFh.Write(data)
			if n < 0 || n > len(data) {
				return io.ErrShortWrite
			}
			if n > 0 {
				data = data[n:]
			}
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
		}
		return nil
	}
	if payloadLen+8 < 1<<32 {
		hdr := make([]byte, 8)
		binary.BigEndian.PutUint32(hdr[0:4], uint32(payloadLen+8))
		copy(hdr[4:8], "mdat")
		if err := writeAll(hdr); err != nil {
			return err
		}
	} else {
		hdr := make([]byte, 16)
		binary.BigEndian.PutUint32(hdr[0:4], 1)
		copy(hdr[4:8], "mdat")
		binary.BigEndian.PutUint64(hdr[8:16], uint64(payloadLen+16))
		if err := writeAll(hdr); err != nil {
			return err
		}
	}

	if _, err := f.Seek(b.Offset+b.HeaderLen, io.SeekStart); err != nil {
		return fmt.Errorf("定位 mdat 数据失败: %w", err)
	}
	var written int64
	for written < payloadLen {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := int64(len(streamBuf))
		if remaining := payloadLen - written; want > remaining {
			want = remaining
		}
		n, err := f.Read(streamBuf[:want])
		if n > 0 {
			if writeErr := writeAll(streamBuf[:n]); writeErr != nil {
				return writeErr
			}
			written += int64(n)
		}
		if err != nil {
			if err == io.EOF && written == payloadLen {
				break
			}
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
	}
	if written != payloadLen {
		return fmt.Errorf("mdat 数据不完整 (实际 %d / 预期 %d): %w", written, payloadLen, io.ErrUnexpectedEOF)
	}
	return nil
}

func copyOrRename(src, dst string) error {
	return copyOrRenameContext(context.Background(), src, dst)
}

// copyPreservingSourceContext writes a durable replacement while keeping src intact.
// This is used when the caller explicitly asks not to delete temporary inputs.
func copyPreservingSourceContext(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()
	sInfo, err := s.Stat()
	if err != nil {
		return err
	}
	if !sInfo.Mode().IsRegular() {
		return fmt.Errorf("源文件不是普通文件: %s", src)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".bbdown-copy-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(sInfo.Mode().Perm()); err != nil {
		return err
	}
	if _, err := copyWithContext(ctx, tmp, s, make([]byte, 1024*1024)); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := copyOrRenameContext(ctx, tmpPath, dst); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func copyOrRenameContext(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// 1. 同卷时优先直接原子重命名。Unix 上这一步也能原子覆盖已有目标，
	// Windows 上目标被占用或跨卷时则进入下面的事务性复制路径。
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	// 2. 跨卷/Windows 降级：先把源文件完整复制到目标目录的临时文件，
	// 同步并关闭后才替换目标，整个过程中绝不先删除唯一的旧成品。
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()
	sInfo, err := s.Stat()
	if err != nil {
		return err
	}
	if !sInfo.Mode().IsRegular() {
		return fmt.Errorf("源文件不是普通文件: %s", src)
	}

	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".bbdown-replace-*")
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
	if err := tmp.Chmod(sInfo.Mode().Perm()); err != nil {
		return err
	}

	if _, err = copyWithContext(ctx, tmp, s, make([]byte, 1024*1024)); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	// 若目标已存在，先移到同目录备份；新文件替换失败时立即恢复。
	var backupPath string
	if _, statErr := os.Stat(dst); statErr == nil {
		backup, err := os.CreateTemp(dir, ".bbdown-backup-*")
		if err != nil {
			return err
		}
		backupPath = backup.Name()
		if err := backup.Close(); err != nil {
			_ = os.Remove(backupPath)
			return err
		}
		if err := os.Remove(backupPath); err != nil {
			return err
		}
		if err := os.Rename(dst, backupPath); err != nil {
			return err
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}

	if err := ctx.Err(); err != nil {
		if backupPath != "" {
			if restoreErr := os.Rename(backupPath, dst); restoreErr != nil {
				return fmt.Errorf("取消替换时恢复旧文件失败: %v", restoreErr)
			}
		}
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		if backupPath != "" {
			if restoreErr := os.Rename(backupPath, dst); restoreErr != nil {
				return fmt.Errorf("替换目标失败: %w；恢复旧文件也失败: %v", err, restoreErr)
			}
		}
		return err
	}
	cleanupTemp = false

	if backupPath != "" {
		if err := os.Remove(backupPath); err != nil {
			return fmt.Errorf("删除旧成品备份失败: %w", err)
		}
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("删除源临时文件失败: %w", err)
	}
	return nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader, buf []byte) (int64, error) {
	if len(buf) == 0 {
		buf = make([]byte, 128*1024)
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			written := 0
			for written < n {
				if err := ctx.Err(); err != nil {
					return total, err
				}
				count, writeErr := dst.Write(buf[written:n])
				if count < 0 || count > n-written {
					return total, io.ErrShortWrite
				}
				if count > 0 {
					written += count
					total += int64(count)
				}
				if writeErr != nil {
					return total, writeErr
				}
				if count == 0 {
					return total, io.ErrShortWrite
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return total, nil
			}
			return total, readErr
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// adjustMoofBox 核心合规性修正：确保每个 traf 的 tfhd 显式声明 default-base-is-moof，并校准 trun 的 DataOffset
func resolveFragmentSampleDefaults(moof *mp4.MoofBox, mvex *mp4.MvexBox, targetTrackID uint32) error {
	for _, traf := range moof.Trafs {
		if traf == nil || traf.Tfhd == nil {
			return fmt.Errorf("moof 缺少 tfhd")
		}
		trackID := traf.Tfhd.TrackID
		if targetTrackID != 0 {
			trackID = targetTrackID
		}
		var trex *mp4.TrexBox
		if mvex != nil {
			for _, candidate := range mvex.Trexs {
				if candidate != nil && candidate.TrackID == trackID {
					trex = candidate
					break
				}
			}
		}
		for _, trun := range traf.Truns {
			if trun == nil {
				return fmt.Errorf("moof 包含空 trun")
			}
			trun.AddSampleDefaultValues(traf.Tfhd, trex)
		}
	}
	return nil
}

func adjustMoofBox(moof *mp4.MoofBox, targetTrackID uint32, mdatHeaderLen int64, seqNum uint32) error {
	if moof == nil {
		return fmt.Errorf("moof 为空")
	}
	if mdatHeaderLen < 8 {
		return fmt.Errorf("mdat 头长度无效: %d", mdatHeaderLen)
	}
	if moof.Mfhd != nil && seqNum > 0 {
		moof.Mfhd.SequenceNumber = seqNum
	}
	for i, traf := range moof.Trafs {
		if traf == nil {
			return fmt.Errorf("traf %d 为空", i)
		}
		if traf.Tfhd != nil {
			if targetTrackID > 0 {
				traf.Tfhd.TrackID = targetTrackID
			}
			// 启用 default-base-is-moof (0x020000)，清除 base-data-offset-present (0x000001)
			traf.Tfhd.Flags = (traf.Tfhd.Flags &^ 0x000001) | 0x020000
			traf.Tfhd.BaseDataOffset = 0
		}
		for j, trun := range traf.Truns {
			if trun == nil {
				return fmt.Errorf("traf %d 的 trun %d 为空", i, j)
			}
			if traf.Tfhd != nil && traf.Tfhd.HasDefaultSampleSize() && !trun.HasSampleSize() {
				for i := range trun.Samples {
					trun.Samples[i].Size = traf.Tfhd.DefaultSampleSize
				}
			}
			trun.Flags |= 0x000001 // 声明 data-offset-present
		}
	}

	moofSize := moof.Size()
	const maxInt32 = int64(1<<31 - 1)
	if moofSize > uint64(maxInt32) || mdatHeaderLen > maxInt32-int64(moofSize) {
		return fmt.Errorf("moof 数据偏移超出 int32 范围")
	}
	currentOffset := int64(moofSize) + mdatHeaderLen

	for _, traf := range moof.Trafs {
		for _, trun := range traf.Truns {
			if currentOffset > maxInt32 {
				return fmt.Errorf("trun 数据偏移超出 int32 范围")
			}
			trun.DataOffset = int32(currentOffset)
			dataSize := trun.SizeOfData()
			if dataSize > uint64(maxInt32) || currentOffset > maxInt32-int64(dataSize) {
				return fmt.Errorf("trun 数据长度超出 int32 范围")
			}
			currentOffset += int64(dataSize)
		}
	}

	return nil
}

// promoteChunkOffsets replaces stco with co64 before the output header is measured.
func promoteChunkOffsets(trak *mp4.TrakBox) {
	if trak == nil || trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil {
		return
	}
	stbl := trak.Mdia.Minf.Stbl
	if stbl.Stco == nil {
		return
	}
	old := stbl.Stco
	co64 := &mp4.Co64Box{Version: old.Version, Flags: old.Flags, ChunkOffset: make([]uint64, len(old.ChunkOffset))}
	for i, offset := range old.ChunkOffset {
		co64.ChunkOffset[i] = uint64(offset)
	}
	for i, box := range stbl.Children {
		if box == old {
			stbl.Children[i] = co64
		}
	}
	stbl.Stco = nil
	stbl.Co64 = co64
}

// relocateChunkOffsets maps each source chunk to its own mdat payload. A constant
// delta cannot handle metadata gaps, multiple mdats, or normalized 64-bit headers.
func relocateChunkOffsets(trak *mp4.TrakBox, boxes []BoxInfo, newStart int64) error {
	if newStart < 0 || trak == nil || trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil {
		return fmt.Errorf("缺少有效 sample table 或输出偏移")
	}
	stbl := trak.Mdia.Minf.Stbl
	if stbl.Co64 == nil {
		return fmt.Errorf("缺少 co64 chunk offset")
	}
	type span struct{ oldStart, oldEnd, newStart uint64 }
	var spans []span
	cursor := newStart
	for _, box := range boxes {
		if box.Type != "mdat" {
			continue
		}
		size := getMdatHeaderLen(box) + int64(box.Size) - box.HeaderLen
		if size < 8 || cursor > math.MaxInt64-size {
			return fmt.Errorf("mdat 输出偏移溢出")
		}
		spans = append(spans, span{uint64(box.Offset + box.HeaderLen), uint64(box.Offset) + box.Size, uint64(cursor + getMdatHeaderLen(box))})
		cursor += size
	}
	if len(spans) == 0 {
		return fmt.Errorf("缺少 mdat 数据")
	}
	offsets := make([]uint64, len(stbl.Co64.ChunkOffset))
	for i, offset := range stbl.Co64.ChunkOffset {
		found := false
		for _, span := range spans {
			if offset >= span.oldStart && offset < span.oldEnd {
				offsets[i] = span.newStart + offset - span.oldStart
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("chunk %d 偏移 %d 不在 mdat 数据中", i, offset)
		}
	}
	stbl.Co64.ChunkOffset = offsets
	return nil
}
