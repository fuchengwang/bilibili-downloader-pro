package downloader

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

func compatibleHEVCInit(t *testing.T) *mp4.InitSegment {
	t.Helper()
	nalu := func(text string) [][]byte {
		b, err := hex.DecodeString(text)
		if err != nil {
			t.Fatal(err)
		}
		return [][]byte{b}
	}
	init := mp4.CreateEmptyInit()
	init.AddEmptyTrack(16000, "video", "und")
	err := init.Moov.Trak.SetHEVCDescriptor("hvc1",
		nalu("40010c01ffff0160000003009000000300000300789ca11024"),
		nalu("420101016000000300900000030000030078a00280802d1659ca1192449af016a020202080000003008000000f04"),
		nalu("4401c1253d9090"), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	init.Moov.Trak.Mdia.Minf.Stbl.Stsd.HvcX.SetType("hev1")
	return init
}

func TestHEVCPlaybackTagRequiresCompleteConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*mp4.VisualSampleEntryBox)
		want  string
	}{
		{"complete", func(e *mp4.VisualSampleEntryBox) {}, "hvc1"},
		{"missing config", func(e *mp4.VisualSampleEntryBox) { e.HvcC = nil }, "hev1"},
		{"missing PPS", func(e *mp4.VisualSampleEntryBox) { e.HvcC.NaluArrays = e.HvcC.NaluArrays[:2] }, "hev1"},
		{"incomplete SPS", func(e *mp4.VisualSampleEntryBox) {
			e.HvcC.NaluArrays[1] = hevc.NewNaluArray(false, hevc.NALU_SPS, e.HvcC.NaluArrays[1].Nalus)
		}, "hev1"},
		{"empty PPS", func(e *mp4.VisualSampleEntryBox) { e.HvcC.NaluArrays[2].Nalus = nil }, "hev1"},
		{"invalid PPS", func(e *mp4.VisualSampleEntryBox) { e.HvcC.NaluArrays[2].Nalus = [][]byte{{0x40, 0x01}} }, "hev1"},
		{"Dolby Vision", func(e *mp4.VisualSampleEntryBox) { e.SetType("dvhe") }, "dvhe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			init := compatibleHEVCInit(t)
			entry := init.Moov.Trak.Mdia.Minf.Stbl.Stsd.HvcX
			tc.alter(entry)
			normalizeHEVCForPlayback(init.Moov)
			if entry.Type() != tc.want {
				t.Fatalf("got %s, want %s", entry.Type(), tc.want)
			}
		})
	}
}

func TestVideoPassthroughNormalizesHEVCWithoutChangingMedia(t *testing.T) {
	for _, deleteSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "keep source", true: "delete source"}[deleteSource], func(t *testing.T) {
			init := compatibleHEVCInit(t)
			var raw bytes.Buffer
			if err := init.Encode(&raw); err != nil {
				t.Fatal(err)
			}
			mdat := &mp4.MdatBox{}
			mdat.SetData([]byte("compressed media must remain byte-identical"))
			if err := mdat.Encode(&raw); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			src, dst := filepath.Join(dir, "v.mp4"), filepath.Join(dir, "out.mp4")
			original := append([]byte(nil), raw.Bytes()...)
			if err := os.WriteFile(src, original, 0644); err != nil {
				t.Fatal(err)
			}
			if err := MergeAudioVideo(src, "", dst, deleteSource); err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(dst)
			if err != nil {
				t.Fatal(err)
			}
			// Exactly one sample-entry fourCC may change. No metadata or payload shifts.
			if !bytes.Equal(actual, bytes.Replace(original, []byte("hev1"), []byte("hvc1"), 1)) {
				t.Fatal("compatibility fix changed media bytes or offsets")
			}
			if deleteSource {
				if _, err := os.Stat(src); !os.IsNotExist(err) {
					t.Fatalf("source remains: %v", err)
				}
			} else if data, err := os.ReadFile(src); err != nil || !bytes.Equal(data, original) {
				t.Fatal("original source was altered")
			}
		})
	}
}
