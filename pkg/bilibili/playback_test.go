package bilibili

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func normalAccessClient(player string) *Client {
	return &Client{wbiMixin: "test-key", wbiCached: time.Now(), httpClient: &http.Client{Transport: playurlTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"code":0,"data":{"dash":{"duration":100,"video":[{"id":80,"codecid":7,"base_url":"https://video.test/full"}],"audio":[{"base_url":"https://audio.test/full"}]}}}`
		if r.URL.Path == "/x/player/v2" {
			body = player
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
}

func TestNormalChargeRights(t *testing.T) {
	for _, tc := range []struct{ name, player, errorKind string }{
		{"uncharged", `{"code":0,"data":{"is_upower_exclusive":true,"is_upower_play":false}}`, "charge_required"},
		{"charged", `{"code":0,"data":{"is_upower_exclusive":true,"is_upower_play":true}}`, ""},
		{"public", `{"code":0,"data":{"is_upower_exclusive":false,"is_upower_play":false}}`, ""},
		{"missing rights", `{"code":0,"data":{"is_upower_exclusive":true}}`, ""},
		{"player error", `{"code":-403}`, ""},
		{"purchase preview", `{"code":0,"data":{"is_ugc_pay_preview":true}}`, "purchase_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := normalAccessClient(tc.player)
			info := c.GetPlaybackInfo(context.Background(), "BVtest", 1, 2, 0, false, false)
			_, err := c.FetchStreamSelection(context.Background(), "BVtest", 1, 2, 0, false, "highest", "auto")
			if tc.errorKind == "" {
				if info.Error != nil || err != nil || len(info.Qualities) == 0 {
					t.Fatalf("playable video incorrectly blocked: info=%+v err=%v", info, err)
				}
			} else {
				if info.Error == nil || info.Error.Kind != tc.errorKind || err == nil || PlaybackErrorInfo(err).Kind != tc.errorKind {
					t.Fatalf("restriction lost: info=%+v err=%v", info, err)
				}
			}
		})
	}
}

func TestExplicitPermissionErrorSurvivesFallbacks(t *testing.T) {
	c := normalAccessClient(`{"code":0,"data":{}}`)
	c.httpClient.Transport = playurlTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"code":0,"data":{}}`
		if r.URL.Path == "/x/player/wbi/playurl" {
			body = `{"code":-10403,"message":"需要大会员"}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	_, err := c.FetchStreamSelection(context.Background(), "BVtest", 1, 2, 0, false, "highest", "auto")
	if err == nil || PlaybackErrorInfo(err).Kind != "vip_required" {
		t.Fatalf("explicit error replaced by fallback: %v", err)
	}
}

func TestUnknownStreamFailureDoesNotClaimCharge(t *testing.T) {
	c := normalAccessClient(`{"code":0,"data":{}}`)
	c.httpClient.Transport = playurlTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{}}`)), Request: r}, nil
	})
	_, err := c.FetchStreamSelection(context.Background(), "BVtest", 1, 2, 0, false, "highest", "auto")
	if err == nil || PlaybackErrorInfo(err).Kind != "unavailable" || strings.Contains(err.Error(), "充电") || strings.Contains(err.Error(), "DASH") {
		t.Fatalf("unknown failure misclassified: %v", err)
	}
}

func TestQualityPickerRejectsPreview(t *testing.T) {
	c := playurlClient(t, `{"code":0,"result":{"is_preview":1,"video_info":{"dash":{"video":[{"id":80,"base_url":"https://video.test/preview"}]}}}}`, nil)
	info := c.GetPlaybackInfo(context.Background(), "", 1, 2, 42, true, false)
	if info.Error == nil || info.Error.Kind != "preview_only" || len(info.Qualities) > 0 {
		t.Fatalf("preview exposed as full quality: %+v", info)
	}
}
