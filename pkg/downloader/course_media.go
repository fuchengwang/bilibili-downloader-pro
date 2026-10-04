package downloader

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Eyevinn/mp4ff/mp4"
)

// prepareCourseStreamContext creates a clear, temporary stream for the existing
// muxer. The downloaded stream is kept intact for pause/resume and error retry.
// Memory use is bounded by one media fragment, rather than the entire lesson.
// The caller removes the returned path when it differs from sourcePath.
func prepareCourseStreamContext(ctx context.Context, sourcePath string, keys map[string][]byte) (clearPath string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			clearPath = ""
			err = fmt.Errorf("课堂媒体分片格式无效")
		}
	}()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return "", err
	}
	boxes, err := scanBoxes(source, info.Size())
	if err != nil {
		return "", err
	}
	var moov *mp4.MoovBox
	for _, box := range boxes {
		if box.Type == "moov" {
			decoded, err := decodeCourseBox(source, box, 8*1024*1024)
			if err != nil {
				return "", err
			}
			moov = decoded.(*mp4.MoovBox)
			break
		}
	}
	if moov == nil || len(moov.Traks) == 0 {
		return "", fmt.Errorf("课堂媒体缺少音视频轨信息")
	}
	encrypted := false
	for _, track := range moov.Traks {
		if track.Tkhd == nil || track.Mdia == nil || track.Mdia.Minf == nil || track.Mdia.Minf.Stbl == nil || track.Mdia.Minf.Stbl.Stsd == nil {
			return "", fmt.Errorf("课堂媒体轨信息不完整")
		}
		for _, entry := range track.Mdia.Minf.Stbl.Stsd.Children {
			if entry.Type() == "encv" || entry.Type() == "enca" {
				encrypted = true
			}
		}
	}
	if !encrypted {
		return sourcePath, nil
	}
	if moov.Mvex == nil || len(moov.Mvex.Trexs) == 0 {
		return "", fmt.Errorf("该课堂媒体封装格式暂不支持")
	}
	decryptInfo, err := mp4.DecryptInit(&mp4.InitSegment{Moov: moov})
	if err != nil {
		return "", fmt.Errorf("课堂媒体初始化失败: %w", err)
	}
	for _, track := range decryptInfo.TrackInfos {
		if track.Sinf == nil {
			continue
		}
		if track.Sinf.Schi == nil || track.Sinf.Schi.Tenc == nil || len(keys[hex.EncodeToString(track.Sinf.Schi.Tenc.DefaultKID)]) != 16 {
			return "", fmt.Errorf("课堂媒体与官方播放授权不匹配，请重新解析课程")
		}
	}
	output, err := os.CreateTemp(filepath.Dir(sourcePath), ".course-clear-*.m4s")
	if err != nil {
		return "", err
	}
	outputPath := output.Name()
	keepOutput := false
	defer func() {
		_ = output.Close()
		if !keepOutput {
			_ = os.Remove(outputPath)
		}
	}()
	writer := courseContextWriter{ctx: ctx, writer: output}
	fragments := 0
	for index := 0; index < len(boxes); index++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		box := boxes[index]
		switch box.Type {
		case "moov":
			if err := moov.Encode(writer); err != nil {
				return "", err
			}
		case "moof":
			if index+1 >= len(boxes) || boxes[index+1].Type != "mdat" {
				return "", fmt.Errorf("课堂媒体分片缺少媒体数据")
			}
			decoded, err := decodeCourseBox(source, box, 8*1024*1024)
			if err != nil {
				return "", err
			}
			mdat, err := decodeCourseBox(source, boxes[index+1], 128*1024*1024)
			if err != nil {
				return "", err
			}
			fragment := mp4.NewFragment()
			fragment.AddChild(decoded)
			fragment.AddChild(mdat)
			if err := mp4.DecryptFragmentWithKeys(fragment, decryptInfo, nil, keys, true); err != nil {
				return "", fmt.Errorf("课堂媒体分片处理失败: %w", err)
			}
			if err := fragment.Encode(writer); err != nil {
				return "", err
			}
			fragments++
			index++
		case "mdat":
			return "", fmt.Errorf("课堂媒体数据未关联到有效分片")
		case "sidx", "mfra", "pssh":
			// These offsets/protection headers refer to the encrypted layout.
		default:
			reader := io.NewSectionReader(source, box.Offset, int64(box.Size))
			if _, err := copyWithContext(ctx, writer, reader, make([]byte, 64*1024)); err != nil {
				return "", err
			}
		}
	}
	if fragments == 0 {
		return "", fmt.Errorf("课堂媒体未包含可播放分片")
	}
	if err := output.Sync(); err != nil {
		return "", err
	}
	if err := output.Close(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	keepOutput = true
	return outputPath, nil
}

func decodeCourseBox(source *os.File, box BoxInfo, maximum uint64) (mp4.Box, error) {
	if box.Size > maximum {
		return nil, fmt.Errorf("课堂媒体 %s 分片过大", box.Type)
	}
	reader := io.NewSectionReader(source, box.Offset+box.HeaderLen, int64(box.Size)-box.HeaderLen)
	header := mp4.BoxHeader{Name: box.Type, Size: box.Size, Hdrlen: int(box.HeaderLen)}
	return mp4.DecodeBoxBody(uint64(box.Offset), header, reader)
}

type courseContextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w courseContextWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(data)
}
