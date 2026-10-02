package downloader

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

// hvc1 is accepted by Apple's native players. Only relabel streams whose hvcC
// explicitly contains complete parameter sets; hev1 may otherwise rely on
// changing in-band parameters, and changing its tag would misrepresent the media.
func canUseHvc1(entry *mp4.VisualSampleEntryBox) bool {
	if entry == nil || entry.Type() != "hev1" || entry.HvcC == nil {
		return false
	}
	var vps, sps, pps bool
	for _, array := range entry.HvcC.NaluArrays {
		switch array.NaluType() {
		case hevc.NALU_VPS, hevc.NALU_SPS, hevc.NALU_PPS:
			if array.Complete() != 1 || len(array.Nalus) == 0 {
				return false
			}
			for _, nalu := range array.Nalus {
				if len(nalu) < 2 || hevc.GetNaluType(nalu[0]) != array.NaluType() {
					return false
				}
			}
			switch array.NaluType() {
			case hevc.NALU_VPS:
				vps = true
			case hevc.NALU_SPS:
				sps = true
			case hevc.NALU_PPS:
				pps = true
			}
		}
	}
	return vps && sps && pps
}

func normalizeHEVCForPlayback(moov *mp4.MoovBox) bool {
	changed := false
	for _, trak := range moov.Traks {
		if trak == nil || trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil || trak.Mdia.Minf.Stbl.Stsd == nil {
			continue
		}
		for _, box := range trak.Mdia.Minf.Stbl.Stsd.Children {
			if entry, ok := box.(*mp4.VisualSampleEntryBox); ok && canUseHvc1(entry) {
				entry.SetType("hvc1")
				changed = true
			}
		}
	}
	return changed
}

// A video-only download still needs the same compatibility fix. Work on the
// transactional copy, and require a byte-exact metadata round trip so chunk
// offsets, unknown boxes and the compressed media payload remain untouched.
func normalizeVideoFileForPlayback(ctx context.Context, f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	boxes, err := scanBoxes(f, info.Size())
	if err != nil {
		return nil // Non-MP4 passthrough retains its original bytes.
	}
	for _, box := range boxes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if box.Type != "moov" || box.Size > 64*1024*1024 {
			continue
		}
		raw := make([]byte, int(box.Size))
		if _, err := f.ReadAt(raw, box.Offset); err != nil {
			return err
		}
		decoded, err := mp4.DecodeBox(uint64(box.Offset), bytes.NewReader(raw))
		if err != nil {
			continue // Unsupported metadata must not damage a passthrough file.
		}
		moov, ok := decoded.(*mp4.MoovBox)
		if !ok {
			continue
		}
		var before bytes.Buffer
		if err := moov.Encode(&before); err != nil || !bytes.Equal(before.Bytes(), raw) {
			continue
		}
		if !normalizeHEVCForPlayback(moov) {
			continue
		}
		var after bytes.Buffer
		if err := moov.Encode(&after); err != nil {
			return err
		}
		if after.Len() != len(raw) {
			return fmt.Errorf("HEVC 兼容性修复改变了元数据长度")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if n, err := f.WriteAt(after.Bytes(), box.Offset); err != nil {
			return err
		} else if n != after.Len() {
			return io.ErrShortWrite
		}
	}
	return nil
}

func copyVideoForPlaybackContext(ctx context.Context, src, dst string, deleteSource bool) error {
	if err := copyPreservingSourceContext(ctx, src, dst); err != nil {
		return err
	}
	if deleteSource {
		srcInfo, srcErr := os.Stat(src)
		dstInfo, dstErr := os.Stat(dst)
		if srcErr == nil && dstErr == nil && !os.SameFile(srcInfo, dstInfo) {
			_ = os.Remove(src)
		}
	}
	return nil
}
