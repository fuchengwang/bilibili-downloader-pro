package downloader

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
)

type progressiveExpectedSample struct {
	mp4.Sample
	data []byte
}

// Fixtures exercise offsets/timing independently of the table-building code.
// Payloads are identifiers, not compressed media; independent decoder tests use
// real encoders in media_validation_test.go and progressive_media_test.go.
func progressiveFixture(t *testing.T, path string, video bool, variant string) []progressiveExpectedSample {
	t.Helper()
	init := mp4.CreateEmptyInit()
	init.Moov.Mvhd.Timescale = 1000
	scale, kind := uint32(44100), "audio"
	if video {
		scale, kind = 16000, "video"
	}
	trak := init.AddEmptyTrack(scale, kind, "und")
	trak.Tkhd.TrackID = 7
	init.Moov.Mvex.Trex.TrackID = 7
	if video {
		sps, _ := hex.DecodeString("67640020accac05005bb0169e0000003002000000c9c4c000432380008647c12401cb1c31380")
		pps, _ := hex.DecodeString("68b5df20")
		if err := trak.SetAVCDescriptor("avc1", [][]byte{sps}, [][]byte{pps}, true); err != nil {
			t.Fatal(err)
		}
		if variant == "avc3" {
			trak.Mdia.Minf.Stbl.Stsd.AvcX.SetType("avc3")
		}
	} else if err := trak.SetAACDescriptor(2, int(scale)); err != nil {
		t.Fatal(err)
	}
	if variant == "movie_scale" && !video {
		init.Moov.Mvhd.Timescale = 48000
		edts := &mp4.EdtsBox{}
		edts.AddChild(&mp4.ElstBox{Entries: []mp4.ElstEntry{
			{SegmentDuration: 24000, MediaTime: -1, MediaRateInteger: 1},
			{MediaTime: 0, MediaRateInteger: 1},
		}})
		trak.AddChild(edts)
	}
	if variant == "hybrid" {
		trak.Mdia.Minf.Stbl.Stsz.SampleNumber = 1
		trak.Mdia.Minf.Stbl.Stsz.SampleSize = []uint32{8}
	}
	if variant == "trex" {
		init.Moov.Mvex.Trex.DefaultSampleDuration = 1024
		init.Moov.Mvex.Trex.DefaultSampleSize = 8
	}
	var output bytes.Buffer
	if err := init.Encode(&output); err != nil {
		t.Fatal(err)
	}
	var expected []progressiveExpectedSample
	var decodeTime uint64
	for seq := uint32(1); seq <= 3; seq++ {
		moof := &mp4.MoofBox{}
		_ = moof.AddChild(mp4.CreateMfhd(seq))
		traf := &mp4.TrafBox{}
		tfhd := mp4.CreateTfhd(7)
		if variant == "absolute" {
			tfhd.Flags |= mp4.TfhdBaseDataOffsetPresentFlag
			tfhd.Flags &^= mp4.TfhdDefaultBaseIsMoofFlag
		}
		_ = traf.AddChild(tfhd)
		if variant != "implicit_time" {
			base := decodeTime
			if variant == "origin" {
				base += 100
			}
			_ = traf.AddChild(mp4.CreateTfdt(base))
		}
		_ = moof.AddChild(traf)
		var payload []byte
		var starts []int
		for ri := 0; ri < 2; ri++ {
			trun := mp4.CreateTrun(uint32(ri + 1))
			if variant == "padding" {
				payload = append(payload, bytes.Repeat([]byte{0xee}, 17)...)
			}
			starts = append(starts, len(payload))
			for si := 0; si < 3; si++ {
				dur, cto, flags := uint32(1024), int32(0), mp4.SyncSampleFlags
				if video {
					dur = []uint32{533, 534, 800}[si]
					cto = []int32{1066, 533, 0}[si]
					if variant == "negative_cts" && si == 1 {
						cto = -533
					}
					if ri != 0 || si != 0 {
						flags = mp4.NonSyncSampleFlags | mp4.SampleDependsOn1
					}
				}
				if variant == "trex" {
					dur = 1024
				}
				data := bytes.Repeat([]byte{byte(seq), byte(ri), byte(si), byte(scale)}, 2)
				s := mp4.NewSample(flags, dur, uint32(len(data)), cto)
				expected = append(expected, progressiveExpectedSample{Sample: s, data: data})
				trun.AddSample(s)
				payload = append(payload, data...)
				decodeTime += uint64(dur)
			}
			if variant == "trex" {
				trun.Flags &^= mp4.TrunSampleDurationPresentFlag | mp4.TrunSampleSizePresentFlag
			}
			_ = traf.AddChild(trun)
		}
		for i, trun := range traf.Truns {
			trun.DataOffset = int32(moof.Size() + 8 + uint64(starts[i]))
			if variant == "absolute" {
				tfhd.BaseDataOffset = uint64(output.Len()) + moof.Size() + 7
				trun.DataOffset = int32(starts[i] + 1)
			}
			if variant == "bad_offset" {
				trun.DataOffset += 100000
			}
		}
		if err := moof.Encode(&output); err != nil {
			t.Fatal(err)
		}
		if err := (&mp4.MdatBox{Data: payload}).Encode(&output); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, output.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return expected
}

func assertProgressiveOutput(t *testing.T, path string) *mp4.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, _ := f.Stat()
	boxes, err := scanBoxes(f, info.Size())
	if err != nil || len(boxes) != 3 || boxes[0].Type != "ftyp" || boxes[1].Type != "moov" || boxes[2].Type != "mdat" {
		t.Fatalf("expected fast-start ordinary MP4, boxes=%+v err=%v", boxes, err)
	}
	_, _ = f.Seek(0, io.SeekStart)
	parsed, err := mp4.DecodeFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Moov.Mvex != nil || len(parsed.Moov.Traks) != 2 {
		t.Fatal("ordinary output must contain two tracks and no fragment defaults")
	}
	for _, track := range parsed.Moov.Traks {
		stbl := track.Mdia.Minf.Stbl
		if stbl.Stts == nil || len(stbl.Stts.SampleCount) == 0 || stbl.Stsz == nil || stbl.Stsz.SampleNumber == 0 ||
			stbl.Stsc == nil || len(stbl.Stsc.Entries) == 0 || (stbl.Stco == nil && stbl.Co64 == nil) || track.Mdia.Mdhd.Duration == 0 {
			t.Fatalf("incomplete sample table for track %d", track.Tkhd.TrackID)
		}
	}
	return parsed
}

func TestProgressiveAVCAACPreservesSamplesAndTiming(t *testing.T) {
	for _, variant := range []string{"timing", "padding", "absolute", "trex", "implicit_time", "movie_scale"} {
		t.Run(variant, func(t *testing.T) {
			dir := t.TempDir()
			v, a, out := filepath.Join(dir, "v.mp4"), filepath.Join(dir, "a.mp4"), filepath.Join(dir, "out.mp4")
			want := [][]progressiveExpectedSample{progressiveFixture(t, v, true, variant), progressiveFixture(t, a, false, variant)}
			if err := MergeAudioVideo(v, a, out, false); err != nil {
				t.Fatal(err)
			}
			parsed := assertProgressiveOutput(t, out)
			raw, _ := os.ReadFile(out)
			for ti, track := range parsed.Moov.Traks {
				stbl := track.Mdia.Minf.Stbl
				samples, err := track.GetSampleData(1, stbl.Stsz.SampleNumber)
				if err != nil || len(samples) != len(want[ti]) {
					t.Fatalf("sample count: %d %v", len(samples), err)
				}
				for i, sample := range samples {
					w := want[ti][i]
					if sample.Dur != w.Dur || sample.Size != w.Size || sample.CompositionTimeOffset != w.CompositionTimeOffset {
						t.Fatalf("sample %d metadata changed: %+v want %+v", i, sample, w.Sample)
					}
					chunk, first, err := stbl.Stsc.ChunkNrFromSampleNr(i + 1)
					if err != nil {
						t.Fatal(err)
					}
					offset := uint64(stbl.Stco.ChunkOffset[chunk-1])
					for j := first - 1; j < i; j++ {
						offset += uint64(samples[j].Size)
					}
					if !bytes.Equal(raw[offset:offset+uint64(sample.Size)], w.data) {
						t.Fatalf("sample %d points to different media bytes", i)
					}
				}
				if ti == 0 && stbl.Ctts.Version != 0 {
					t.Fatal("nonnegative B-frame offsets should use the most compatible ctts version")
				}
			}
			if variant == "movie_scale" {
				elst := parsed.Moov.Traks[1].Edts.Elst[0]
				if parsed.Moov.Mvhd.Timescale != 48000 || elst.Entries[0].SegmentDuration != 24000 {
					t.Fatal("audio delay changed when joining different movie timescales")
				}
			}
			if _, err := os.Stat(v); err != nil {
				t.Fatal("deleteTemp=false removed source")
			}
		})
	}
}

func TestProgressiveUnsupportedLayoutsKeepExistingMuxer(t *testing.T) {
	for _, variant := range []string{"avc3", "origin", "negative_cts"} {
		t.Run(variant, func(t *testing.T) {
			dir := t.TempDir()
			v, a, out := filepath.Join(dir, "v.mp4"), filepath.Join(dir, "a.mp4"), filepath.Join(dir, "out.mp4")
			progressiveFixture(t, v, true, variant)
			progressiveFixture(t, a, false, variant)
			if err := MergeAudioVideo(v, a, out, false); err != nil {
				t.Fatal(err)
			}
			f, _ := os.Open(out)
			defer f.Close()
			parsed, err := mp4.DecodeFile(f)
			if err != nil || parsed.Moov.Mvex == nil || len(parsed.Segments) == 0 {
				t.Fatalf("unsupported layout did not retain original path: %v", err)
			}
		})
	}
}

func TestProgressiveRejectsInvalidSamplesPreservingFiles(t *testing.T) {
	for _, variant := range []string{"bad_offset", "hybrid"} {
		t.Run(variant, func(t *testing.T) {
			dir := t.TempDir()
			v, a, out := filepath.Join(dir, "v.mp4"), filepath.Join(dir, "a.mp4"), filepath.Join(dir, "out.mp4")
			progressiveFixture(t, v, true, variant)
			progressiveFixture(t, a, false, "timing")
			_ = os.WriteFile(out, []byte("existing"), 0600)
			if err := MergeAudioVideo(v, a, out, true); err == nil {
				t.Fatal("invalid/unsupported hybrid samples must not publish a broken success")
			}
			data, _ := os.ReadFile(out)
			if string(data) != "existing" {
				t.Fatal("failure replaced existing output")
			}
			for _, path := range []string{v, a} {
				if _, err := os.Stat(path); err != nil {
					t.Fatal("failure removed an input")
				}
			}
		})
	}
}

type progressiveAbortWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
	err    error
}

func (w *progressiveAbortWriter) Write(b []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.Buffer.Write(b)
	if len(b) == 8 && string(b[4:8]) == "mdat" {
		w.cancel()
	}
	return n, err
}

func TestProgressiveCancellationAndLargeOffsets(t *testing.T) {
	init := mp4.CreateEmptyInit()
	trak := init.AddEmptyTrack(1000, "video", "und")
	track := &progressiveTrack{trak: trak, samples: []progressiveSample{{Sample: mp4.Sample{Size: 4, Dur: 1000}}}}
	chunks := interleaveProgressiveChunks([]*progressiveTrack{track})
	if err := buildProgressiveSampleTable(track, chunks, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &progressiveAbortWriter{cancel: cancel}
	if err := writeProgressiveMP4(ctx, w, init.Moov, []*progressiveTrack{track}, chunks); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel after metadata should stop before copying media: %v", err)
	}
	chunks[0].size = 1 << 32
	stop := errors.New("stop before copying a large sparse payload")
	if err := writeProgressiveMP4(context.Background(), &progressiveAbortWriter{err: stop}, init.Moov, []*progressiveTrack{track}, chunks); !errors.Is(err, stop) {
		t.Fatal(err)
	}
	stbl := trak.Mdia.Minf.Stbl
	if stbl.Stco != nil || stbl.Co64 == nil || len(stbl.Co64.ChunkOffset) != 1 || stbl.Co64.ChunkOffset[0] <= init.Moov.Size()+16 {
		t.Fatal("large output did not select co64 before measuring its header")
	}
}

func TestProgressiveWriteFailurePreservesExistingOutput(t *testing.T) {
	dir := t.TempDir()
	src, out := filepath.Join(dir, "short.mdat"), filepath.Join(dir, "out.mp4")
	_ = os.WriteFile(src, []byte("short"), 0600)
	_ = os.WriteFile(out, []byte("existing"), 0600)
	f, _ := os.Open(src)
	defer f.Close()
	init := mp4.CreateEmptyInit()
	trak := init.AddEmptyTrack(1000, "video", "und")
	track := &progressiveTrack{file: f, trak: trak, samples: []progressiveSample{{Sample: mp4.Sample{Size: 100, Dur: 1000}}}}
	chunks := interleaveProgressiveChunks([]*progressiveTrack{track})
	if err := buildProgressiveSampleTable(track, chunks, 0); err != nil {
		t.Fatal(err)
	}
	if err := publishProgressiveMP4(context.Background(), init.Moov, []*progressiveTrack{track}, chunks, out, true); err == nil {
		t.Fatal("truncated media must fail while writing the temporary output")
	}
	data, _ := os.ReadFile(out)
	if string(data) != "existing" {
		t.Fatal("partial media replaced the old output")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatal("write failure removed source")
	}
	files, _ := filepath.Glob(filepath.Join(dir, ".tmp_mrg_*"))
	if len(files) != 0 {
		t.Fatal("write failure left temporary output")
	}
}
