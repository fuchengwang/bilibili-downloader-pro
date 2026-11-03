package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

// Manual acceptance probe: identical URL/headers/byte ranges, varied transport.
// Signed URLs are exported only to a private 0600 reference input; cookies
// are not exported. Neither enters the printable measurement results.
func TestLiveTransportComparison(t *testing.T) {
	if testing.Short() || os.Getenv("BBDOWN_TRANSPORT_COMPARE") != "1" {
		t.Skip("opt-in live transport comparison")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	selection, err := bilibili.GetDefaultClient().FetchStreamSelection(ctx, "BV1xx411c7mD", 0, 62131, 0, false, "32", "AVC")
	if err != nil {
		t.Fatal(diagnosticURLPattern.ReplaceAllString(err.Error(), "[hidden]"))
	}
	ref := struct {
		Files []struct {
			VideoURL string            `json:"videoUrl"`
			Headers  map[string]string `json:"headers"`
		} `json:"files"`
	}{Files: []struct {
		VideoURL string            `json:"videoUrl"`
		Headers  map[string]string `json:"headers"`
	}{{VideoURL: selection.VideoURLs[0], Headers: map[string]string{"referer": bilibili.Referer, "user-agent": bilibili.BrowserUA}}}}
	payload, _ := json.Marshal(ref)
	if err := os.WriteFile("/tmp/bbdown-transport-reference.json", payload, 0600); err != nil {
		t.Fatal(err)
	}
	targets := []struct {
		name, url string
		headers   map[string]string
	}{{"api-primary-video", selection.VideoURLs[0], nil}, {"api-backup-video", selection.VideoURLs[len(selection.VideoURLs)-1], nil}, {"api-primary-audio", selection.AudioURLs[0], nil}}
	if os.Getenv("BBDOWN_COMPARE_REFERENCE_ONLY") == "1" {
		targets = targets[:1]
	}
	for _, candidate := range selection.VideoURLs {
		u, _ := url.Parse(candidate)
		t.Log("candidate", u.Hostname())
	}
	for _, target := range targets {
		u, _ := url.Parse(target.url)
		for _, mode := range []string{"shared", "isolated"} {
			shared := mediaTransport.Clone()
			shared.Proxy = nil
			var network, requests, connections, h2, errorsN atomic.Int64
			run, stop := context.WithTimeout(ctx, 8*time.Second)
			start := time.Now()
			var wg sync.WaitGroup
			for worker := 0; worker < 4; worker++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					tr := shared
					if mode == "isolated" {
						tr = shared.Clone()
						defer tr.CloseIdleConnections()
					}
					request, _ := http.NewRequestWithContext(run, "GET", target.url, nil)
					setMediaHeaders(request)
					for k, v := range target.headers {
						request.Header.Set(k, v)
					}
					request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", worker*2*1024*1024, (worker+1)*2*1024*1024-1))
					request.Header.Set("Accept-Encoding", "identity")
					request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
						t.Log("remote", mode, i.Conn.RemoteAddr().String())
						if !i.Reused {
							connections.Add(1)
						}
					}}))
					resp, err := (&http.Client{Transport: tr}).Do(request)
					if err != nil {
						errorsN.Add(1)
						t.Log("request error", mode, diagnosticURLPattern.ReplaceAllString(err.Error(), "[hidden]"))
						return
					}
					defer resp.Body.Close()
					requests.Add(1)
					if resp.StatusCode != 206 {
						errorsN.Add(1)
						return
					}
					if resp.ProtoMajor == 2 {
						h2.Add(1)
					}
					buffer := make([]byte, 64*1024)
					for {
						n, err := resp.Body.Read(buffer)
						network.Add(int64(n))
						if err != nil {
							if err != io.EOF && run.Err() == nil {
								errorsN.Add(1)
							}
							return
						}
					}
				}()
			}
			wg.Wait()
			stop()
			shared.CloseIdleConnections()
			t.Logf("target=%s host=%s mode=%s bytes=%d seconds=%.2f KiB/s=%.1f requests=%d newTCP=%d h2=%d errors=%d", target.name, u.Hostname(), mode, network.Load(), time.Since(start).Seconds(), float64(network.Load())/time.Since(start).Seconds()/1024, requests.Load(), connections.Load(), h2.Load(), errorsN.Load())
		}
	}
}
