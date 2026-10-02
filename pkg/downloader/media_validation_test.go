package downloader

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Independent encoders/decoders validate sample offsets and audio preservation, not just MP4 boxes.
// Run locally with BBDOWN_MEDIA_TESTS=1 go test ./pkg/downloader -run TestGeneratedMediaMerge -v.
func TestGeneratedMediaMerge(t *testing.T) {
	if os.Getenv("BBDOWN_MEDIA_TESTS") != "1" {
		t.Skip("opt-in independent media validation requires ffmpeg and ffprobe")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, encoder, audio string
		fragmented           bool
	}{
		{"AVC_classic_AAC", "libx264", "aac", false},
		{"AVC_fragmented_AAC", "libx264", "aac", true},
		{"HEVC_fragmented_AAC", "libx265", "aac", true},
		{"AV1_fragmented_AAC", "libaom-av1", "aac", true},
		{"AVC_fragmented_FLAC", "libx264", "flac", true},
		{"AVC_fragmented_Dolby", "libx264", "eac3", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			v, a, out := filepath.Join(dir, "v.mp4"), filepath.Join(dir, "a.mp4"), filepath.Join(dir, "out.mp4")
			run := func(bin string, args ...string) []byte {
				t.Helper()
				b, err := exec.Command(bin, args...).CombinedOutput()
				if err != nil {
					t.Fatalf("%s failed: %v\n%s", filepath.Base(bin), err, b)
				}
				return b
			}
			args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=128x96:rate=24", "-t", "2", "-an", "-c:v", tc.encoder, "-threads", "2"}
			if tc.encoder == "libaom-av1" {
				args = append(args, "-cpu-used", "8")
			}
			if tc.fragmented && tc.encoder != "libaom-av1" {
				args = append(args, "-movflags", "+frag_keyframe+empty_moov+default_base_moof")
			}
			run(ffmpeg, append(args, "-y", v)...)
			if tc.encoder == "libaom-av1" {
				// Generate the AV1 configuration first; empty_moov while encoding emits
				// an empty av1C in some FFmpeg versions, which is an invalid source.
				source := filepath.Join(dir, "av1-source.mp4")
				if err := os.Rename(v, source); err != nil {
					t.Fatal(err)
				}
				run(ffmpeg, "-v", "error", "-i", source, "-c", "copy", "-movflags", "+frag_keyframe+empty_moov+default_base_moof", "-y", v)
			}
			args = []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000", "-t", "2", "-vn", "-c:a", tc.audio}
			if tc.audio == "flac" {
				a = filepath.Join(dir, "a.flac")
			} else if tc.fragmented {
				flags := "+frag_keyframe+empty_moov+default_base_moof"
				if tc.audio == "eac3" {
					flags = "+frag_keyframe+delay_moov+default_base_moof"
				}
				args = append(args, "-movflags", flags)
			}
			run(ffmpeg, append(args, "-y", a)...)
			if err := MergeAudioVideo(v, a, out, false); err != nil {
				t.Fatal(err)
			}
			data := run(ffprobe, "-v", "error", "-show_entries", "stream=codec_type,codec_name,duration", "-of", "json", out)
			var probe struct {
				Streams []struct {
					Type     string `json:"codec_type"`
					Codec    string `json:"codec_name"`
					Duration string `json:"duration"`
				} `json:"streams"`
			}
			if err := json.Unmarshal(data, &probe); err != nil {
				t.Fatal(err)
			}
			if len(probe.Streams) != 2 {
				t.Fatalf("missing tracks: %s", data)
			}
			for _, stream := range probe.Streams {
				dur, err := strconv.ParseFloat(stream.Duration, 64)
				if err != nil || dur < 1.9 || dur > 2.2 {
					t.Fatalf("unexpected track duration: %+v", stream)
				}
			}
			// Fatal decode errors must fail the test, even if the file has two track headers.
			decoded := run(ffmpeg, "-v", "error", "-xerror", "-i", out, "-map", "0:v:0", "-map", "0:a:0", "-f", "null", "-")
			if strings.TrimSpace(string(decoded)) != "" {
				t.Fatalf("decoder errors: %s", decoded)
			}
			t.Logf("two tracks, complete durations, and full independent decode: %s", tc.name)
		})
	}
}
