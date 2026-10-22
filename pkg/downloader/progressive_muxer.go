package downloader

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"sort"

	"bilibili_downloader/pkg/utils"

	"github.com/Eyevinn/mp4ff/mp4"
)

// A complete sample table makes downloaded AVC/AAC files readable without a
// fragment scan. Other codecs and layouts continue through the existing muxer.
var errProgressiveLayout = errors.New("需保留原分片封装")

type progressiveSample struct {
	mp4.Sample
	offset int64
}

type progressiveTrack struct {
	file       *os.File
	trak       *mp4.TrakBox
	movieScale uint32
	samples    []progressiveSample
	duration   uint64
	mediaEnd   uint64
}

type progressiveChunk struct {
	track      int
	first      int
	count      uint32
	decodeTime uint64
	offset     int64
	size       uint64
	index      int
}

func progressiveSourceTrack(moov *mp4.MoovBox, handler, codec string) *mp4.TrakBox {
	if moov == nil || moov.Mvhd == nil || moov.Mvhd.Timescale == 0 || len(moov.Traks) != 1 {
		return nil
	}
	t := moov.Traks[0]
	if t == nil || t.Tkhd == nil || t.Tkhd.TrackID == 0 || t.Mdia == nil || t.Mdia.Mdhd == nil || t.Mdia.Mdhd.Timescale == 0 ||
		t.Mdia.Hdlr == nil || t.Mdia.Hdlr.HandlerType != handler || t.Mdia.Minf == nil || t.Mdia.Minf.Stbl == nil {
		return nil
	}
	stbl := t.Mdia.Minf.Stbl
	if stbl.Stsd == nil || len(stbl.Stsd.Children) != 1 || stbl.Stsd.Children[0].Type() != codec {
		return nil
	}
	switch entry := stbl.Stsd.Children[0].(type) {
	case *mp4.VisualSampleEntryBox:
		if entry.AvcC == nil || entry.Sinf != nil || entry.DataReferenceIndex != 1 {
			return nil
		}
	case *mp4.AudioSampleEntryBox:
		if entry.Esds == nil || entry.Esds.DecConfigDescriptor == nil || entry.Esds.DecConfigDescriptor.ObjectType != 0x40 || entry.Sinf != nil || entry.DataReferenceIndex != 1 {
			return nil
		}
	default:
		return nil
	}
	for _, child := range t.Children {
		if child.Type() == "tref" {
			return nil
		}
	}
	if t.Edts != nil {
		if len(t.Edts.Elst) != 1 || len(t.Edts.Elst[0].Entries) == 0 || len(t.Edts.Elst[0].Entries) > 2 {
			return nil
		}
		edits := t.Edts.Elst[0].Entries
		if edits[len(edits)-1].MediaTime < 0 || (len(edits) == 2 && edits[0].MediaTime != -1) {
			return nil
		}
		for i, edit := range t.Edts.Elst[0].Entries {
			if edit.MediaRateInteger != 1 || edit.MediaRateFraction != 0 || edit.MediaTime < -1 ||
				(edit.MediaTime == -1 && (i != 0 || edit.SegmentDuration == 0)) {
				return nil
			}
		}
	}
	return t
}

func hasMovieFragments(boxes []BoxInfo) bool {
	for _, box := range boxes {
		if box.Type == "moof" {
			return true
		}
	}
	return false
}

func tryMergeProgressiveAVCAAC(ctx context.Context, video, audio *os.File, videoBoxes, audioBoxes []BoxInfo, videoMoov, audioMoov *mp4.MoovBox, output string, deleteTemp bool) (bool, error) {
	v := progressiveSourceTrack(videoMoov, "vide", "avc1")
	a := progressiveSourceTrack(audioMoov, "soun", "mp4a")
	if v == nil || a == nil || !hasMovieFragments(videoBoxes) || !hasMovieFragments(audioBoxes) {
		return false, nil
	}
	movieScale, ok := commonMovieScale(videoMoov.Mvhd.Timescale, audioMoov.Mvhd.Timescale)
	if !ok || v.Tkhd.TrackID >= math.MaxUint32-1 {
		return false, nil
	}
	vt, err := collectProgressiveTrack(ctx, video, videoBoxes, videoMoov, v)
	if errors.Is(err, errProgressiveLayout) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("建立视频样本索引失败: %w", err)
	}
	at, err := collectProgressiveTrack(ctx, audio, audioBoxes, audioMoov, a)
	if errors.Is(err, errProgressiveLayout) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("建立音频样本索引失败: %w", err)
	}
	tracks := []*progressiveTrack{vt, at}
	chunks := interleaveProgressiveChunks(tracks)
	for i, track := range tracks {
		if err := buildProgressiveSampleTable(track, chunks, i); err != nil {
			return true, err
		}
		if err := setProgressiveDuration(track, movieScale); err != nil {
			return true, err
		}
	}
	a.Tkhd.TrackID = v.Tkhd.TrackID + 1
	videoMoov.AddChild(a)
	videoMoov.Mvhd.NextTrackID = a.Tkhd.TrackID + 1
	videoMoov.Mvhd.Timescale = movieScale
	videoMoov.Mvhd.Duration = max(v.Tkhd.Duration, a.Tkhd.Duration)
	if videoMoov.Mvhd.Duration > math.MaxUint32 {
		videoMoov.Mvhd.Version = 1
	}
	children := videoMoov.Children[:0]
	for _, child := range videoMoov.Children {
		if child.Type() != "mvex" {
			children = append(children, child)
		}
	}
	videoMoov.Children, videoMoov.Mvex = children, nil
	return true, publishProgressiveMP4(ctx, videoMoov, tracks, chunks, output, deleteTemp)
}

func commonMovieScale(a, b uint32) (uint32, bool) {
	x, y := a, b
	for y != 0 {
		x, y = y, x%y
	}
	if x == 0 {
		return 0, false
	}
	lcm := uint64(a/x) * uint64(b)
	return uint32(lcm), lcm <= math.MaxUint32
}

func collectProgressiveTrack(ctx context.Context, f *os.File, boxes []BoxInfo, moov *mp4.MoovBox, trak *mp4.TrakBox) (*progressiveTrack, error) {
	stbl := trak.Mdia.Minf.Stbl
	if stbl.Stsz == nil || stbl.Stsz.SampleNumber != 0 || (stbl.Stts != nil && len(stbl.Stts.SampleCount) != 0) {
		// The old fragment path drops the initial mdat of hybrid files. Fail
		// explicitly instead of falling back to a known incomplete output.
		return nil, fmt.Errorf("暂不支持含初始普通样本的混合分片 MP4")
	}
	if (stbl.Ctts != nil && len(stbl.Ctts.SampleOffset) != 0) || (stbl.Sdtp != nil && len(stbl.Sdtp.Entries) != 0) ||
		(stbl.Stss != nil && len(stbl.Stss.SampleNumber) != 0) {
		return nil, errProgressiveLayout
	}
	// Do not discard groups, encryption or dependency metadata that require
	// conversion beyond the ordinary independent AVC/AAC sample tables.
	for _, child := range stbl.Children {
		switch child.Type() {
		case "stsd", "stts", "ctts", "stsc", "stsz", "stco", "co64", "stss", "sdtp":
		default:
			return nil, errProgressiveLayout
		}
	}
	t := &progressiveTrack{file: f, trak: trak, movieScale: moov.Mvhd.Timescale}
	var trex *mp4.TrexBox
	if moov.Mvex != nil {
		for _, candidate := range moov.Mvex.Trexs {
			if candidate != nil && candidate.TrackID == trak.Tkhd.TrackID {
				trex = candidate
				break
			}
		}
	}
	var mdats []BoxInfo
	for _, box := range boxes {
		if box.Type == "mdat" {
			mdats = append(mdats, box)
		}
	}
	for _, box := range boxes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if box.Type != "moof" {
			continue
		}
		if box.Size > 64*1024*1024 {
			return nil, fmt.Errorf("分片索引过大")
		}
		decoded, err := mp4.DecodeBox(uint64(box.Offset), io.NewSectionReader(f, box.Offset, int64(box.Size)))
		if err != nil {
			return nil, err
		}
		moof, ok := decoded.(*mp4.MoofBox)
		if !ok || len(moof.Trafs) != 1 {
			return nil, errProgressiveLayout
		}
		traf := moof.Trafs[0]
		if traf == nil || traf.Tfhd == nil || traf.Tfhd.TrackID != trak.Tkhd.TrackID {
			return nil, fmt.Errorf("分片轨道与初始化信息不匹配")
		}
		for _, child := range traf.Children {
			switch child.Type() {
			case "tfhd", "tfdt", "trun":
			default:
				return nil, errProgressiveLayout
			}
		}
		if traf.Tfdt != nil && traf.Tfdt.BaseMediaDecodeTime() != t.duration {
			// Keep discontinuities and nonzero media origins in their original
			// layout until their edit-list conversion is supported explicitly.
			return nil, errProgressiveLayout
		}
		tfhd := traf.Tfhd
		if tfhd.HasSampleDescriptionIndex() && tfhd.SampleDescriptionIndex != 1 {
			return nil, errProgressiveLayout
		}
		if trex != nil && !tfhd.HasSampleDescriptionIndex() && trex.DefaultSampleDescriptionIndex != 1 {
			return nil, errProgressiveLayout
		}
		base := box.Offset
		if tfhd.HasBaseDataOffset() {
			if tfhd.BaseDataOffset > math.MaxInt64 {
				return nil, fmt.Errorf("分片数据偏移溢出")
			}
			base = int64(tfhd.BaseDataOffset)
		}
		next := base
		for _, trun := range traf.Truns {
			if trun == nil {
				return nil, fmt.Errorf("分片缺少有效样本表")
			}
			trun.AddSampleDefaultValues(tfhd, trex)
			if trun.HasDataOffset() {
				delta := int64(trun.DataOffset)
				if (delta > 0 && base > math.MaxInt64-delta) || (delta < 0 && base < -delta) {
					return nil, fmt.Errorf("样本数据偏移溢出")
				}
				next = base + delta
			}
			for _, sample := range trun.Samples {
				if len(t.samples)%1024 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				if sample.CompositionTimeOffset < 0 {
					// Fragment and classic demuxers normalize signed CTS offsets
					// differently. Keep this layout until its presentation origin
					// can be converted without shifting any packet timestamps.
					return nil, errProgressiveLayout
				}
				if sample.Size == 0 || sample.Dur == 0 || next < 0 || next > math.MaxInt64-int64(sample.Size) ||
					!sampleWithinMdat(mdats, next, int64(sample.Size)) {
					return nil, fmt.Errorf("样本大小、时长或数据边界无效")
				}
				if len(t.samples) >= math.MaxUint32 || t.duration > math.MaxInt64-uint64(sample.Dur)-math.MaxInt32 {
					return nil, fmt.Errorf("样本数量或时间溢出")
				}
				end := int64(t.duration) + int64(sample.CompositionTimeOffset) + int64(sample.Dur)
				if end > 0 {
					t.mediaEnd = max(t.mediaEnd, uint64(end))
				}
				t.samples = append(t.samples, progressiveSample{Sample: sample, offset: next})
				t.duration += uint64(sample.Dur)
				next += int64(sample.Size)
			}
		}
	}
	if len(t.samples) == 0 {
		return nil, fmt.Errorf("分片不包含可播放样本")
	}
	return t, nil
}

func sampleWithinMdat(mdats []BoxInfo, offset, size int64) bool {
	i := sort.Search(len(mdats), func(i int) bool { return mdats[i].Offset > offset }) - 1
	return i >= 0 && offset >= mdats[i].Offset+mdats[i].HeaderLen && offset+size <= mdats[i].Offset+int64(mdats[i].Size)
}

func interleaveProgressiveChunks(tracks []*progressiveTrack) []progressiveChunk {
	var chunks []progressiveChunk
	for ti, t := range tracks {
		var decodeTime uint64
		for first := 0; first < len(t.samples); {
			chunk := progressiveChunk{track: ti, first: first, decodeTime: decodeTime, offset: t.samples[first].offset}
			var dur uint64
			for first+int(chunk.count) < len(t.samples) {
				s := t.samples[first+int(chunk.count)]
				if chunk.count > 0 && (dur*2 >= uint64(t.trak.Mdia.Mdhd.Timescale) || s.offset != chunk.offset+int64(chunk.size)) {
					break
				}
				chunk.count++
				chunk.size += uint64(s.Size)
				dur += uint64(s.Dur)
			}
			chunks = append(chunks, chunk)
			first += int(chunk.count)
			decodeTime += dur
		}
	}
	sort.SliceStable(chunks, func(i, j int) bool {
		a, b := chunks[i], chunks[j]
		ah, al := bits.Mul64(a.decodeTime, uint64(tracks[b.track].trak.Mdia.Mdhd.Timescale))
		bh, bl := bits.Mul64(b.decodeTime, uint64(tracks[a.track].trak.Mdia.Mdhd.Timescale))
		return ah < bh || (ah == bh && al < bl)
	})
	return chunks
}

func buildProgressiveSampleTable(t *progressiveTrack, chunks []progressiveChunk, ti int) error {
	stbl := mp4.NewStblBox()
	stbl.AddChild(t.trak.Mdia.Minf.Stbl.Stsd)
	stts, ctts := &mp4.SttsBox{}, &mp4.CttsBox{EndSampleNr: []uint32{0}}
	stsz := &mp4.StszBox{SampleNumber: uint32(len(t.samples))}
	stss, sdtp := &mp4.StssBox{}, &mp4.SdtpBox{}
	hasCTO := false
	for i, s := range t.samples {
		if n := len(stts.SampleCount); n > 0 && stts.SampleTimeDelta[n-1] == s.Dur {
			stts.SampleCount[n-1]++
		} else {
			stts.SampleCount = append(stts.SampleCount, 1)
			stts.SampleTimeDelta = append(stts.SampleTimeDelta, s.Dur)
		}
		if n := len(ctts.SampleOffset); n > 0 && ctts.SampleOffset[n-1] == s.CompositionTimeOffset {
			ctts.EndSampleNr[n]++
		} else {
			ctts.SampleOffset = append(ctts.SampleOffset, s.CompositionTimeOffset)
			ctts.EndSampleNr = append(ctts.EndSampleNr, uint32(i+1))
		}
		hasCTO = hasCTO || s.CompositionTimeOffset != 0
		if s.CompositionTimeOffset < 0 {
			ctts.Version = 1
		}
		stsz.SampleSize = append(stsz.SampleSize, s.Size)
		if t.trak.Mdia.Hdlr.HandlerType == "vide" {
			f := mp4.DecodeSampleFlags(s.Flags)
			if !f.SampleIsNonSync {
				stss.SampleNumber = append(stss.SampleNumber, uint32(i+1))
			}
			sdtp.Entries = append(sdtp.Entries, mp4.SdtpEntry(f.IsLeading<<6|f.SampleDependsOn<<4|f.SampleIsDependedOn<<2|f.SampleHasRedundancy))
		}
	}
	stbl.AddChild(stts)
	if hasCTO {
		stbl.AddChild(ctts)
	}
	stbl.AddChild(stsz)
	if t.trak.Mdia.Hdlr.HandlerType == "vide" {
		stbl.AddChild(stss)
		stbl.AddChild(sdtp)
	}
	stsc, stco := &mp4.StscBox{}, &mp4.StcoBox{}
	for i := range chunks {
		if chunks[i].track != ti {
			continue
		}
		chunks[i].index = len(stco.ChunkOffset)
		stco.ChunkOffset = append(stco.ChunkOffset, 0)
		if n := len(stsc.Entries); n == 0 || stsc.Entries[n-1].SamplesPerChunk != chunks[i].count {
			if err := stsc.AddEntry(uint32(len(stco.ChunkOffset)), chunks[i].count, 1); err != nil {
				return err
			}
		}
	}
	stbl.AddChild(stsc)
	stbl.AddChild(stco)
	minf := t.trak.Mdia.Minf
	for i, child := range minf.Children {
		if child == minf.Stbl {
			minf.Children[i] = stbl
		}
	}
	minf.Stbl = stbl
	return nil
}

func rescaleProgressiveDuration(value uint64, from, to uint32) (uint64, error) {
	hi, lo := bits.Mul64(value, uint64(to))
	if from == 0 || hi >= uint64(from) {
		return 0, fmt.Errorf("媒体时长换算溢出")
	}
	q, r := bits.Div64(hi, lo, uint64(from))
	if r != 0 {
		if q == math.MaxUint64 {
			return 0, fmt.Errorf("媒体时长换算溢出")
		}
		q++
	}
	return q, nil
}

func setProgressiveDuration(t *progressiveTrack, movieScale uint32) error {
	mdhd, tkhd := t.trak.Mdia.Mdhd, t.trak.Tkhd
	mdhd.Duration = max(t.duration, t.mediaEnd)
	if mdhd.Duration > math.MaxUint32 {
		mdhd.Version = 1
	}
	var err error
	tkhd.Duration, err = rescaleProgressiveDuration(t.mediaEnd, mdhd.Timescale, movieScale)
	if err != nil {
		return err
	}
	if t.trak.Edts != nil {
		tkhd.Duration = 0
		for _, elst := range t.trak.Edts.Elst {
			for i := range elst.Entries {
				e := &elst.Entries[i]
				if e.SegmentDuration == 0 {
					if e.MediaTime < 0 || uint64(e.MediaTime) >= t.mediaEnd {
						return fmt.Errorf("媒体编辑列表超出样本时间范围")
					}
					e.SegmentDuration, err = rescaleProgressiveDuration(t.mediaEnd-uint64(e.MediaTime), mdhd.Timescale, movieScale)
				} else {
					e.SegmentDuration, err = rescaleProgressiveDuration(e.SegmentDuration, t.movieScale, movieScale)
				}
				if err != nil || tkhd.Duration > math.MaxUint64-e.SegmentDuration {
					return fmt.Errorf("媒体编辑列表时长溢出")
				}
				tkhd.Duration += e.SegmentDuration
				if e.SegmentDuration > math.MaxUint32 || e.MediaTime > math.MaxInt32 {
					elst.Version = 1
				}
			}
		}
	}
	if tkhd.Duration > math.MaxUint32 {
		tkhd.Version = 1
	}
	return nil
}

func writeProgressiveMP4(ctx context.Context, dst io.Writer, moov *mp4.MoovBox, tracks []*progressiveTrack, chunks []progressiveChunk) error {
	ftyp := mp4.NewFtyp("isom", 512, []string{"isom", "iso2", "avc1", "mp41"})
	var payload uint64
	for _, chunk := range chunks {
		if chunk.size > math.MaxInt64 || payload > math.MaxInt64-chunk.size {
			return fmt.Errorf("媒体数据大小溢出")
		}
		payload += chunk.size
	}
	header := uint64(8)
	if payload >= math.MaxUint32-7 {
		header = 16
	}
	// Fix the table width before calculating final offsets, including files
	// whose moov/header growth pushes a chunk across the 4 GB boundary.
	if ftyp.Size()+moov.Size()+header > math.MaxInt64-payload {
		return fmt.Errorf("输出文件大小溢出")
	}
	if ftyp.Size()+moov.Size()+header+payload > math.MaxUint32 {
		for _, t := range tracks {
			promoteChunkOffsets(t.trak)
		}
	}
	offset := ftyp.Size() + moov.Size() + header
	if offset > math.MaxInt64-payload {
		return fmt.Errorf("输出文件偏移溢出")
	}
	for _, chunk := range chunks {
		stbl := tracks[chunk.track].trak.Mdia.Minf.Stbl
		if stbl.Co64 != nil {
			stbl.Co64.ChunkOffset[chunk.index] = offset
		} else {
			stbl.Stco.ChunkOffset[chunk.index] = uint32(offset)
		}
		offset += chunk.size
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ftyp.Encode(dst); err != nil {
		return err
	}
	if err := moov.Encode(dst); err != nil {
		return err
	}
	var raw [16]byte
	copy(raw[4:8], "mdat")
	if header == 16 {
		binary.BigEndian.PutUint32(raw[:4], 1)
		binary.BigEndian.PutUint64(raw[8:], payload+header)
	} else {
		binary.BigEndian.PutUint32(raw[:4], uint32(payload+header))
	}
	if _, err := dst.Write(raw[:header]); err != nil {
		return err
	}
	buf := make([]byte, 1024*1024)
	for _, chunk := range chunks {
		src := io.NewSectionReader(tracks[chunk.track].file, chunk.offset, int64(chunk.size))
		n, err := copyWithContext(ctx, dst, src, buf)
		if err != nil {
			return err
		}
		if uint64(n) != chunk.size {
			return io.ErrUnexpectedEOF
		}
	}
	return ctx.Err()
}

func publishProgressiveMP4(ctx context.Context, moov *mp4.MoovBox, tracks []*progressiveTrack, chunks []progressiveChunk, output string, deleteTemp bool) error {
	if existing, err := os.Stat(output); err == nil {
		for _, t := range tracks {
			if source, err := t.file.Stat(); err == nil && os.SameFile(existing, source) {
				return fmt.Errorf("输出文件不能与音视频源文件相同")
			}
		}
	}
	f, err := os.CreateTemp(filepath.Dir(output), ".tmp_mrg_*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if err := f.Chmod(0644); err != nil {
		return err
	}
	if err := writeProgressiveMP4(ctx, f, moov, tracks, chunks); err != nil {
		return fmt.Errorf("写入兼容 MP4 失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := copyOrRenameContext(ctx, f.Name(), output); err != nil {
		return err
	}
	if deleteTemp {
		for _, t := range tracks {
			_ = t.file.Close()
			utils.SafeRemoveWithRetry(t.file.Name())
		}
	}
	return nil
}
