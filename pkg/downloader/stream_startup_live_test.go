package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
	"github.com/Eyevinn/mp4ff/mp4"
)

// Explicit opt-in. Uses the saved Bilibili session and isolated temporary media;
// never changes the app's queue or prints cookies / signed CDN addresses.
// The selected collection entries are 2 and 3, including every nested part.
func TestLiveCollection360PStartupRecovery(t *testing.T) {
	link := os.Getenv("BBDOWN_STARTUP_TEST_URL")
	if testing.Short() || link == "" {
		t.Skip("set BBDOWN_STARTUP_TEST_URL to verify collection entries 2 and 3")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Fatal("live MP4 validation requires ffprobe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := bilibili.GetDefaultClient()
	target, err := client.ParseInput(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := client.FetchVideoDetail(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if detail.CollectionID == 0 || len(detail.Episodes) < 3 {
		t.Fatal("test requires a UGC collection with at least three entries")
	}
	var episodes []bilibili.EpisodeInfo
	for _, entry := range detail.Episodes[1:3] {
		video, err := client.FetchCollectionVideoParts(ctx, entry)
		if err != nil {
			t.Fatal(err)
		}
		episodes = append(episodes, video.Parts...)
	}
	if len(episodes) == 0 {
		t.Fatal("no nested parts resolved")
	}
	t.Logf("resolved %d parts from collection entries 2 and 3; injected startup faults=%t", len(episodes), os.Getenv("BBDOWN_STARTUP_TEST_FAULTS") == "1")
	slots := make(chan struct{}, 3)
	t.Run("downloads", func(t *testing.T) {
		for _, ep := range episodes {
			t.Run(fmt.Sprintf("%s-P%d", ep.BVID, ep.Page), func(t *testing.T) {
				t.Parallel()
				select {
				case slots <- struct{}{}:
					defer func() { <-slots }()
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				sel, err := client.FetchStreamSelection(ctx, ep.BVID, ep.AID, ep.CID, 0, false, "16", "auto")
				if err != nil {
					t.Fatal(err)
				}
				if sel.QualityID != 16 {
					t.Fatalf("requested 360P, got quality %d", sel.QualityID)
				}
				dir := t.TempDir()
				videoPath, audioPath, out := filepath.Join(dir, "video.tmp"), filepath.Join(dir, "audio.tmp"), filepath.Join(dir, "output.mp4")
				v := NewStreamDownloader(sel.VideoURLs, videoPath)
				defer v.client.CloseIdleConnections()
				v.SetURLRefresher(func(ctx context.Context) ([]string, error) {
					fresh, err := client.FetchStreamSelection(ctx, ep.BVID, ep.AID, ep.CID, 0, false, "16", "auto")
					if err != nil {
						return nil, err
					}
					return fresh.VideoURLs, nil
				})
				var probeFaults, connectionFaults atomic.Int32
				if os.Getenv("BBDOWN_STARTUP_TEST_FAULTS") == "1" {
					base := v.client.Transport
					v.client.Transport = streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
						if r.Header.Get("Range") == "bytes=0-0" {
							probeFaults.Add(1)
							return nil, io.EOF
						}
						if connectionFaults.Add(1) <= 2 {
							return nil, io.EOF
						}
						return base.RoundTrip(r)
					})
				}
				if err := v.DownloadWithConcurrency(ctx, 4, nil); err != nil {
					t.Fatal(diagnosticURLPattern.ReplaceAllString(err.Error(), "[hidden]"))
				}
				var audioBytes int64
				if len(sel.AudioURLs) > 0 {
					a := NewStreamDownloader(sel.AudioURLs, audioPath)
					defer a.client.CloseIdleConnections()
					if err := a.DownloadSingleStream(ctx, nil); err != nil {
						t.Fatal(diagnosticURLPattern.ReplaceAllString(err.Error(), "[hidden]"))
					}
					audioBytes = a.totalSize
				} else {
					audioPath = ""
				}
				if err := MergeAudioVideoContext(ctx, videoPath, audioPath, out, false); err != nil {
					t.Fatal(err)
				}
				f, err := os.Open(out)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				parsed, err := mp4.DecodeFile(f)
				if err != nil || parsed.Moov == nil {
					t.Fatalf("output MP4 cannot be decoded: %v", err)
				}
				handlers := map[string]bool{}
				for _, tr := range parsed.Moov.Traks {
					if tr.Mdia == nil || tr.Mdia.Hdlr == nil || tr.Mdia.Mdhd == nil || tr.Mdia.Mdhd.Timescale == 0 {
						t.Fatal("output track missing metadata")
					}
					handlers[strings.TrimSpace(tr.Mdia.Hdlr.HandlerType)] = true
				}
				if !handlers["vide"] || (audioPath != "" && !handlers["soun"]) {
					t.Fatalf("missing output tracks: %v", handlers)
				}
				// Fragmented MP4 may legally store duration in its fragments,
				// leaving mdhd.duration=0. Scan actual packets independently.
				probe, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-count_packets", "-show_entries", "stream=codec_type,width,height,nb_read_packets,duration", "-of", "json", out).Output()
				if err != nil {
					t.Fatalf("independent MP4 validation failed: %v", err)
				}
				var media struct {
					Streams []struct {
						Type    string `json:"codec_type"`
						Packets string `json:"nb_read_packets"`
						Width   int    `json:"width"`
						Height  int    `json:"height"`
					} `json:"streams"`
				}
				if err := json.Unmarshal(probe, &media); err != nil {
					t.Fatal(err)
				}
				if len(media.Streams) != len(parsed.Moov.Traks) {
					t.Fatal("independent stream count mismatch")
				}
				for _, stream := range media.Streams {
					n, err := strconv.ParseInt(stream.Packets, 10, 64)
					if err != nil || n <= 0 {
						t.Fatalf("track has no readable packets: %+v", stream)
					}
					if stream.Type == "video" && (stream.Width <= 0 || stream.Height <= 0) {
						t.Fatal("video dimensions missing")
					}
				}
				if os.Getenv("BBDOWN_STARTUP_TEST_FAULTS") == "1" && (probeFaults.Load() == 0 || connectionFaults.Load() < 3) {
					t.Fatal("startup faults were not exercised")
				}
				t.Logf("verified 360P MP4: cid=%d video=%d audio=%d tracks=%d injectedProbeEOF=%d", ep.CID, v.totalSize, audioBytes, len(parsed.Moov.Traks), probeFaults.Load())
			})
		}
	})
}
