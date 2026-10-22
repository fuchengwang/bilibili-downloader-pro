package downloader

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
)

func runProgressiveMediaTool(t *testing.T, tool string, args ...string) []byte {
	t.Helper()
	data, err := exec.Command(tool, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", tool, err, data)
	}
	return data
}

func assertProgressiveMediaEqual(t *testing.T, source, output string, streams ...string) {
	t.Helper()
	for _, stream := range streams {
		var before, after struct {
			Packets []struct {
				PTS      int64  `json:"pts"`
				DTS      int64  `json:"dts"`
				Duration int64  `json:"duration"`
				Size     string `json:"size"`
				Hash     string `json:"data_hash"`
			} `json:"packets"`
		}
		probe := func(path string, value any) {
			data := runProgressiveMediaTool(t, "ffprobe", "-v", "error", "-select_streams", stream,
				"-show_packets", "-show_data_hash", "sha256", "-show_entries", "packet=pts,dts,duration,size,data_hash", "-of", "json", path)
			if err := json.Unmarshal(data, value); err != nil {
				t.Fatal(err)
			}
		}
		probe(source, &before)
		probe(output, &after)
		if len(before.Packets) == 0 || !reflect.DeepEqual(before, after) {
			t.Fatalf("%s packet data/timing changed: source=%s output=%s", stream, source, output)
		}
		decode := func(path string) []string {
			data := runProgressiveMediaTool(t, "ffmpeg", "-v", "error", "-xerror", "-copyts", "-i", path,
				"-map", "0:"+stream+":0", "-fps_mode", "passthrough", "-f", "framemd5", "-")
			var frames []string
			for _, line := range strings.Split(string(data), "\n") {
				if line != "" && !strings.HasPrefix(line, "#") {
					frames = append(frames, line)
				}
			}
			return frames
		}
		if !reflect.DeepEqual(decode(source), decode(output)) {
			t.Fatalf("%s decoded frames/audio or their timestamps changed", stream)
		}
		t.Logf("%s: %d compressed packets and full decoded media match", stream, len(before.Packets))
	}
}

func TestProgressiveGeneratedMediaTiming(t *testing.T) {
	if os.Getenv("BBDOWN_MEDIA_TESTS") != "1" {
		t.Skip("opt-in independent media validation requires ffmpeg and ffprobe")
	}
	for _, tc := range []struct {
		name, rate string
		vfr, delay bool
	}{
		{"B_frames_30", "30", false, false},
		{"fractional_2997", "30000/1001", false, false},
		{"60fps", "60", false, false},
		{"variable_frame_rate", "30", true, false},
		{"audio_delay_different_movie_scale", "30", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			v, a, out := filepath.Join(dir, "v.mp4"), filepath.Join(dir, "a.mp4"), filepath.Join(dir, "out.mp4")
			flags := "+frag_keyframe+empty_moov+default_base_moof"
			if tc.delay {
				flags = "+frag_keyframe+delay_moov+default_base_moof"
			}
			args := []string{"-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=" + tc.rate,
				"-t", "6", "-c:v", "libx264", "-threads", "2", "-preset", "veryfast", "-bf", "3", "-g", "30", "-sc_threshold", "0"}
			if tc.vfr {
				args = append(args, "-vf", `select=not(mod(n\,2))+not(mod(n\,5))`, "-fps_mode", "vfr")
			}
			args = append(args, "-movflags", flags, v)
			runProgressiveMediaTool(t, "ffmpeg", args...)
			args = []string{"-v", "error", "-f", "lavfi", "-i", "sine=frequency=997:sample_rate=44100", "-t", "6", "-c:a", "aac"}
			if tc.delay {
				args = append(args, "-af", "asetpts=PTS+0.5/TB", "-movie_timescale", "48000")
			}
			args = append(args, "-movflags", flags, "-frag_duration", "1000000", a)
			runProgressiveMediaTool(t, "ffmpeg", args...)
			if err := MergeAudioVideo(v, a, out, false); err != nil {
				t.Fatal(err)
			}
			assertProgressiveOutput(t, out)
			assertProgressiveMediaEqual(t, v, out, "v")
			assertProgressiveMediaEqual(t, a, out, "a")
		})
	}
}

// The optional local fixture is not stored in the repository. Split it without
// retiming/re-encoding, then exercise the same two-source API used by downloads.
func TestProgressiveLocalMediaFixture(t *testing.T) {
	path := os.Getenv("BBDOWN_MEDIA_FIXTURE")
	if path == "" {
		t.Skip("set BBDOWN_MEDIA_FIXTURE to a local two-track fragmented MP4")
	}
	dir := t.TempDir()
	v, a, out := filepath.Join(dir, "v.mp4"), filepath.Join(dir, "a.mp4"), filepath.Join(dir, "out.mp4")
	splitProgressiveLocalFixture(t, path, v, a)
	if err := MergeAudioVideo(v, a, out, false); err != nil {
		t.Fatal(err)
	}
	assertProgressiveOutput(t, out)
	assertProgressiveMediaEqual(t, path, out, "v", "a")
}

func splitProgressiveLocalFixture(t *testing.T, path, video, audio string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, _ := f.Stat()
	boxes, err := scanBoxes(f, info.Size())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct{ path, handler string }{{video, "vide"}, {audio, "soun"}} {
		out, err := os.Create(target.path)
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		var id uint32
		for i, box := range boxes {
			switch box.Type {
			case "ftyp":
				if _, err := io.Copy(out, io.NewSectionReader(f, box.Offset, int64(box.Size))); err != nil {
					t.Fatal(err)
				}
			case "moov":
				decoded, err := mp4.DecodeBox(uint64(box.Offset), io.NewSectionReader(f, box.Offset, int64(box.Size)))
				if err != nil {
					t.Fatal(err)
				}
				moov := decoded.(*mp4.MoovBox)
				var track *mp4.TrakBox
				for _, candidate := range moov.Traks {
					if candidate.Mdia.Hdlr.HandlerType == target.handler {
						track, id = candidate, candidate.Tkhd.TrackID
					}
				}
				if track == nil || moov.Mvex == nil {
					t.Fatal("local fixture must be a fragmented AVC/AAC file")
				}
				var trex *mp4.TrexBox
				for _, candidate := range moov.Mvex.Trexs {
					if candidate.TrackID == id {
						trex = candidate
					}
				}
				if trex == nil {
					t.Fatal("local fixture lacks track defaults")
				}
				moov.Mvex = mp4.NewMvexBox()
				moov.Mvex.AddChild(trex)
				children := moov.Children[:0]
				for _, child := range moov.Children {
					if child.Type() == "trak" && child != track {
						continue
					}
					if child.Type() == "mvex" {
						child = moov.Mvex
					}
					children = append(children, child)
				}
				moov.Children, moov.Trak, moov.Traks = children, track, []*mp4.TrakBox{track}
				if err := moov.Encode(out); err != nil {
					t.Fatal(err)
				}
			case "moof":
				decoded, err := mp4.DecodeBox(uint64(box.Offset), io.NewSectionReader(f, box.Offset, int64(box.Size)))
				if err != nil {
					t.Fatal(err)
				}
				moof := decoded.(*mp4.MoofBox)
				if len(moof.Trafs) != 1 || moof.Traf.Tfhd.HasBaseDataOffset() || !moof.Traf.Tfhd.DefaultBaseIfMoof() {
					t.Fatal("local splitter requires independent relative-offset fragments")
				}
				if moof.Traf.Tfhd.TrackID != id {
					continue
				}
				if i+1 >= len(boxes) || boxes[i+1].Type != "mdat" {
					t.Fatal("local fixture has no adjacent fragment payload")
				}
				if err := moof.Encode(out); err != nil {
					t.Fatal(err)
				}
				mdat := boxes[i+1]
				if _, err := io.Copy(out, io.NewSectionReader(f, mdat.Offset, int64(mdat.Size))); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
