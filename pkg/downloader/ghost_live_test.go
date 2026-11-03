package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

type cutMediaBody struct {
	io.ReadCloser
	remaining int64
}

func (b *cutMediaBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n, err := b.ReadCloser.Read(p[:min(int64(len(p)), b.remaining)])
	b.remaining -= int64(n)
	return n, err
}

// Opt-in real CDN validation. Uses the existing Bilibili session, writes into
// an isolated QA directory, and does not add anything to the user's queue.
func TestGhostLiveDownload(t *testing.T) {
	url := os.Getenv("BBDOWN_GHOST_LIVE_URL")
	if testing.Short() || url == "" {
		t.Skip("set BBDOWN_GHOST_LIVE_URL for real CDN/hash/decode validation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	fatal := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(diagnosticURLPattern.ReplaceAllString(err.Error(), "[hidden]"))
		}
	}
	client := bilibili.GetDefaultClient()
	target, err := client.ParseInput(ctx, url)
	fatal(err)
	detail, err := client.FetchVideoDetail(ctx, target)
	fatal(err)
	if len(detail.Episodes) == 0 {
		t.Fatal("no episodes")
	}
	ep := detail.Episodes[0]
	if ep.BVID == "" {
		ep.BVID = detail.BVID
	}
	if ep.AID == 0 {
		ep.AID = detail.AID
	}
	selection, err := client.FetchStreamSelection(ctx, ep.BVID, ep.AID, ep.CID, ep.EPID, detail.Type == "bangumi", "16", "AVC")
	fatal(err)
	if selection.QualityID != 16 {
		t.Fatalf("expected bounded 360P validation, got quality %d", selection.QualityID)
	}
	dir := os.Getenv("BBDOWN_GHOST_QA_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	fatal(os.MkdirAll(dir, 0755))
	videoPath, audioPath, out := filepath.Join(dir, "video.downloading"), filepath.Join(dir, "audio.downloading"), filepath.Join(dir, "ghost-core-verified.mp4")
	video := NewStreamDownloader(selection.VideoURLs, videoPath)
	video.SetURLRefresher(func(ctx context.Context) ([]string, error) {
		fresh, err := client.FetchStreamSelection(ctx, ep.BVID, ep.AID, ep.CID, ep.EPID, detail.Type == "bangumi", "16", "AVC")
		if err != nil {
			return nil, err
		}
		return fresh.VideoURLs, nil
	})
	var probes, connections, cuts atomic.Int32
	base := video.client.Transport
	video.client.Transport = streamRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Range") == "bytes=0-0" {
			probes.Add(1)
			return nil, io.EOF
		}
		if connections.Add(1) <= 2 {
			return nil, io.EOF
		}
		resp, err := base.RoundTrip(r)
		if err == nil && (resp.StatusCode == 200 || resp.StatusCode == 206) && cuts.CompareAndSwap(0, 1) {
			resp.Body = &cutMediaBody{ReadCloser: resp.Body, remaining: 32768}
		}
		return resp, err
	})
	start := time.Now()
	fatal(video.DownloadWithConcurrency(ctx, 4, nil))
	if probes.Load() == 0 || connections.Load() < 3 || cuts.Load() != 1 {
		t.Fatal("startup/disconnection faults were not exercised")
	}
	videoHash, videoBytes, err := hashFile(videoPath)
	fatal(err)
	// Compare to a separate, ordinary HTTP GET, without the new range engine.
	var referenceHash string
	var referenceBytes int64
	for _, url := range selection.VideoURLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		setMediaHeaders(req)
		resp, err := (&http.Client{Transport: mediaTransport}).Do(req)
		if err != nil {
			continue
		}
		if resp.StatusCode != 200 || resp.ContentLength <= 0 {
			resp.Body.Close()
			continue
		}
		h := sha256.New()
		n, err := io.Copy(h, resp.Body)
		resp.Body.Close()
		if err == nil && n == resp.ContentLength {
			referenceHash = hex.EncodeToString(h.Sum(nil))
			referenceBytes = n
			break
		}
	}
	if referenceHash == "" || referenceHash != videoHash || referenceBytes != videoBytes {
		t.Fatal("real video hash differs from an independent HTTP GET")
	}
	var audioBytes int64
	if len(selection.AudioURLs) > 0 {
		audio := NewStreamDownloader(selection.AudioURLs, audioPath)
		fatal(audio.DownloadSingleStream(ctx, nil))
		_, audioBytes, err = hashFile(audioPath)
		fatal(err)
	} else {
		audioPath = ""
	}
	fatal(MergeAudioVideoContext(ctx, videoPath, audioPath, out, false))
	ffprobe, err := exec.LookPath("ffprobe")
	fatal(err)
	probe, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-count_packets", "-show_entries", "stream=codec_type,width,height,nb_read_packets:format=duration", "-of", "json", out).Output()
	fatal(err)
	var media struct {
		Streams []struct {
			Type    string `json:"codec_type"`
			Packets string `json:"nb_read_packets"`
			Width   int    `json:"width"`
			Height  int    `json:"height"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	fatal(json.Unmarshal(probe, &media))
	hasVideo, hasAudio := false, false
	for _, stream := range media.Streams {
		n, err := strconv.Atoi(stream.Packets)
		fatal(err)
		if n <= 0 {
			t.Fatal("empty output track")
		}
		if stream.Type == "video" {
			hasVideo = stream.Width > 0 && stream.Height > 0
		}
		if stream.Type == "audio" {
			hasAudio = true
		}
	}
	if !hasVideo || (audioPath != "" && !hasAudio) {
		t.Fatal("merged MP4 lost media tracks")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	fatal(err)
	decode, err := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-xerror", "-i", out, "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("full decode failed: %v %s", err, decode)
	}
	report := fmt.Sprintf("BV=%s CID=%d quality=%d codec=%s\nvideo=%d audio=%d\nsha256=%s\nindependent_http_sha256=%s\nduration=%s tracks=%d\ninjected_probe_errors=%d injected_connect_errors=2 injected_body_cut=%d\nfull_decode=passed elapsed=%s\n", ep.BVID, ep.CID, selection.QualityID, selection.Codec, videoBytes, audioBytes, videoHash, referenceHash, media.Format.Duration, len(media.Streams), probes.Load(), cuts.Load(), time.Since(start).Round(time.Millisecond))
	fatal(os.WriteFile(filepath.Join(dir, "verification.txt"), []byte(report), 0644))
	t.Log(report)
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}
