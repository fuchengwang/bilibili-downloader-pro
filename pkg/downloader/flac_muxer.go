package downloader

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

// IsRawFlac 检查指定文件是否为原生裸 FLAC 音频文件 (支持以 "fLaC" 签名或带 "ID3" 前置标签的文件)
func IsRawFlac(filePath string) bool {
	f, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer f.Close()

	var buf [10]byte
	n, err := io.ReadFull(f, buf[:])
	if err != nil && err != io.ErrUnexpectedEOF {
		return false
	}
	if n < 4 {
		return false
	}

	if string(buf[:4]) == "fLaC" {
		return true
	}

	// 兼容 ID3v2 前置标签
	if n >= 10 && string(buf[:3]) == "ID3" {
		id3Size := int(buf[6])<<21 | int(buf[7])<<14 | int(buf[8])<<7 | int(buf[9])
		if _, err := f.Seek(int64(10+id3Size), io.SeekStart); err != nil {
			return false
		}
		var magic [4]byte
		if _, err := io.ReadFull(f, magic[:]); err == nil && string(magic[:]) == "fLaC" {
			return true
		}
	}

	return false
}

// PackageRawFlacToFMP4 将裸 FLAC 音频流转换为标准的 fMP4 音频流
// 采用 ISO BMFF fLaC / dfLa 规范，无需外部工具，纯 Go 毫秒级封装
func PackageRawFlacToFMP4(rawFlacPath, outFmp4Path string) error {
	f, err := os.Open(rawFlacPath)
	if err != nil {
		return fmt.Errorf("打开裸 FLAC 文件失败: %w", err)
	}
	defer f.Close()

	// 1. 校验并读取 FLAC 头部与 STREAMINFO 元数据 (兼容 ID3v2 标签)
	var buf [4]byte
	if _, err := io.ReadFull(f, buf[:]); err != nil {
		return fmt.Errorf("读取 FLAC 头部失败: %w", err)
	}

	if string(buf[:3]) == "ID3" {
		var restHeader [6]byte
		if _, err := io.ReadFull(f, restHeader[:]); err != nil {
			return fmt.Errorf("读取 ID3v2 头部失败: %w", err)
		}
		id3Size := int(restHeader[2])<<21 | int(restHeader[3])<<14 | int(restHeader[4])<<7 | int(restHeader[5])
		if _, err := f.Seek(int64(10+id3Size), io.SeekStart); err != nil {
			return fmt.Errorf("跳过 ID3v2 标签失败: %w", err)
		}
		if _, err := io.ReadFull(f, buf[:]); err != nil {
			return fmt.Errorf("读取 FLAC 签名失败: %w", err)
		}
	}

	if string(buf[:]) != "fLaC" {
		return fmt.Errorf("非法 FLAC 签名")
	}

	// 解析首个元数据块 (规范必须为 STREAMINFO)
	var streamInfoBytes []byte
	var streamInfo *meta.StreamInfo

	for {
		var hdr [4]byte
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			return fmt.Errorf("读取 FLAC 元数据块头失败: %w", err)
		}
		isLast := (hdr[0] & 0x80) != 0
		blockType := hdr[0] & 0x7F
		length := int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])

		data := make([]byte, length)
		if _, err := io.ReadFull(f, data); err != nil {
			return fmt.Errorf("读取 FLAC 元数据块数据失败: %w", err)
		}

		if blockType == 0 && streamInfoBytes == nil { // STREAMINFO 块
			streamInfoBytes = data
			blockReader := bytes.NewReader(append(hdr[:], data...))
			blk, err := meta.Parse(blockReader)
			if err != nil {
				return fmt.Errorf("解析 STREAMINFO 失败: %w", err)
			}
			si, ok := blk.Body.(*meta.StreamInfo)
			if !ok {
				return fmt.Errorf("STREAMINFO 转换失败")
			}
			streamInfo = si
		}

		if isLast {
			break
		}
	}

	if streamInfo == nil || len(streamInfoBytes) < 34 {
		return fmt.Errorf("缺失有效的 STREAMINFO 元数据")
	}

	// 2. 初始化目标 fMP4 输出文件
	outFh, err := os.OpenFile(outFmp4Path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("创建输出 fMP4 文件失败: %w", err)
	}
	defer outFh.Close()

	timescale := streamInfo.SampleRate
	const audioTrackID = uint32(1)

	// 3. 构建 ftyp 和 moov (Init Segment)
	initSeg := mp4.CreateEmptyInit()
	trak := initSeg.AddEmptyTrack(timescale, "audio", "und")
	trak.Tkhd.TrackID = audioTrackID

	dfla := &mp4.DfLaBox{
		Version: 0,
		Flags:   0,
		MetadataBlocks: []mp4.FLACMetadataBlock{
			{
				LastMetadataBlockFlag: true,
				BlockType:             0,
				Length:                uint32(len(streamInfoBytes)),
				BlockData:             streamInfoBytes,
			},
		},
	}

	// 安全处理采样率：ISO BMFF 规范中 AudioSampleEntry.samplerate 为 16.16 定点数，
	// 当实际采样率 > 65535 Hz 时（如 Hi-Res 96kHz / 192kHz），uint16 入参会溢出截断。
	// 策略：先传入安全的 uint16 值创建 Box，再通过 uint32 字段覆写正确值。
	sampleRateParam := uint16(timescale)
	if timescale > 65535 {
		sampleRateParam = 0 // ISO 14496-12: 0 表示采样率由编解码器专有 box (dfLa) 声明
	}
	flacEntry := mp4.CreateAudioSampleEntryBox(
		"fLaC",
		uint16(streamInfo.NChannels),
		uint16(streamInfo.BitsPerSample),
		sampleRateParam,
		dfla,
	)
	// 使用 mp4ff 内部 uint32 字段覆写，确保编码时写入正确的 16.16 定点采样率
	flacEntry.SampleRate = sampleRateParam
	trak.Mdia.Minf.Stbl.Stsd.AddChild(flacEntry)

	// 写入 Init Segment (ftyp + moov)
	if err := initSeg.Encode(outFh); err != nil {
		return fmt.Errorf("写入 fMP4 Init Segment 失败: %w", err)
	}

	// 4. 流式逐帧封装 FLAC 音频帧至 Fragment (moof + mdat)
	seqNr := uint32(1)
	currentDecodeTime := uint64(0)
	totalFrames := 0

	// 每 ~50 帧打包为一个 Fragment 提升流式效率
	const framesPerFragment = 50

	for {
		var frag *mp4.Fragment
		framesInFrag := 0
		for framesInFrag < framesPerFragment {
			var frameBuf bytes.Buffer
			tee := io.TeeReader(f, &frameBuf)

			flacFrame, parseErr := frame.Parse(tee)
			if parseErr != nil {
				if parseErr == io.EOF || parseErr == io.ErrUnexpectedEOF {
					break
				}
				break
			}

			frameData := frameBuf.Bytes()
			if len(frameData) == 0 {
				break
			}

			if frag == nil {
				var createErr error
				frag, createErr = mp4.CreateFragment(seqNr, audioTrackID)
				if createErr != nil {
					return fmt.Errorf("创建 fMP4 Fragment 失败: %w", createErr)
				}
				seqNr++
			}

			blockSize := uint32(flacFrame.BlockSize)
			if blockSize == 0 {
				blockSize = uint32(streamInfo.BlockSizeMax)
			}

			frag.AddFullSample(mp4.FullSample{
				Sample: mp4.Sample{
					Flags:                 0x02000000, // Sync sample
					Dur:                   blockSize,
					Size:                  uint32(len(frameData)),
					CompositionTimeOffset: 0,
				},
				DecodeTime: currentDecodeTime,
				Data:       frameData,
			})

			currentDecodeTime += uint64(blockSize)
			framesInFrag++
			totalFrames++
		}

		if frag != nil {
			if err := frag.Encode(outFh); err != nil {
				return fmt.Errorf("写入 fMP4 Fragment 失败: %w", err)
			}
		}

		if framesInFrag == 0 {
			break
		}
	}

	if totalFrames == 0 {
		_ = outFh.Close()
		_ = os.Remove(outFmp4Path)
		return fmt.Errorf("FLAC 音频流不完整或损坏: 未解析到任何有效音频帧")
	}

	_ = outFh.Sync()
	return nil
}
