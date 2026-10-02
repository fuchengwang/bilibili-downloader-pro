package bilibili

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type playurlTransport func(*http.Request) (*http.Response, error)

func (f playurlTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func playurlClient(t *testing.T, body string, check func(*http.Request)) *Client {
	t.Helper()
	return &Client{httpClient: &http.Client{Transport: playurlTransport(func(r *http.Request) (*http.Response, error) {
		if check != nil {
			check(r)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
}

func TestBangumiRequestsHighestQuality(t *testing.T) {
	client := playurlClient(t, `{"code":0,"result":{"video_info":{"dash":{"video":[{"id":120,"codecid":12,"base_url":"https://video.test/4k"}],"audio":[{"base_url":"https://audio.test/aac"}]}}}}`, func(r *http.Request) {
		if r.URL.Query().Get("qn") != "127" || r.URL.Query().Get("ep_id") != "42" {
			t.Errorf("missing highest quality or episode: %s", r.URL.RawQuery)
		}
	})
	sel, err := client.FetchStreamSelection(context.Background(), "", 1, 2, 42, true, "highest", "auto")
	if err != nil || sel.QualityID != 120 {
		t.Fatalf("selection=%+v, err=%v", sel, err)
	}
}
func TestPlayurlCamelCaseStreams(t *testing.T) {
	client := playurlClient(t, `{"code":0,"result":{"video_info":{"dash":{"video":[{"id":80,"codecid":7,"baseUrl":"https://video.test/v","backupUrl":["https://backup.test/v"]}],"audio":[{"id":30280,"baseUrl":"https://audio.test/a"}]}}}}`, nil)
	sel, err := client.FetchStreamSelection(context.Background(), "", 1, 2, 42, true, "highest", "auto")
	if err != nil || sel.VideoURL == "" || sel.AudioURL == "" {
		t.Fatalf("camel case URLs lost: %+v %v", sel, err)
	}
}
func TestBangumiRejectsErrorWithStreamPayload(t *testing.T) {
	client := playurlClient(t, `{"code":-10403,"message":"需要大会员","result":{"video_info":{"dash":{"video":[{"id":80,"codecid":7,"base_url":"https://video.test/v"}]}}}}`, nil)
	if _, err := client.FetchStreamSelection(context.Background(), "", 1, 2, 42, true, "highest", "auto"); err == nil {
		t.Fatal("nonzero code must not be accepted")
	}
}
func TestBangumiCodecMismatchDoesNotClaimVIP(t *testing.T) {
	client := playurlClient(t, `{"code":0,"result":{"video_info":{"dash":{"video":[{"id":80,"codecid":7,"base_url":"https://video.test/v"}]}}}}`, nil)
	_, err := client.FetchStreamSelection(context.Background(), "", 1, 2, 42, true, "highest", "HEVC")
	if err == nil || strings.Contains(err.Error(), "大会员") {
		t.Fatalf("codec mismatch falsely reported as VIP: %v", err)
	}
}
func TestBangumiRejectsPreview(t *testing.T) {
	client := playurlClient(t, `{"code":0,"result":{"is_preview":1,"video_info":{"dash":{"duration":6,"video":[{"id":80,"codecid":7,"base_url":"https://video.test/preview"}]}}}}`, nil)
	if _, err := client.FetchStreamSelection(context.Background(), "", 1, 2, 42, true, "highest", "auto"); err == nil {
		t.Fatal("preview must not become a completed full episode")
	}
}
func TestBangumiQualityWithoutAcceptList(t *testing.T) {
	client := playurlClient(t, `{"code":0,"result":{"video_info":{"dash":{"video":[{"id":80,"codecid":7,"base_url":"https://video.test/v"}]}}}}`, nil)
	qs, err := client.GetAvailableQualities(context.Background(), "", 1, 2, 42, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, q := range qs {
		if q.ID == 80 && q.IsAvailable {
			found = true
		}
	}
	if !found {
		t.Fatalf("actual DASH stream unavailable in quality picker: %+v", qs)
	}
}
func TestQualitiesPropagateRequestError(t *testing.T) {
	client := playurlClient(t, `{"code":-10403,"message":"需要大会员"}`, nil)
	if _, err := client.GetAvailableQualities(context.Background(), "", 1, 2, 42, true); err == nil {
		t.Fatal("API error silently hidden")
	}
}

func TestBangumiMemberCookieAndHighQualitySelection(t *testing.T) {
	client := playurlClient(t, `{"code":0,"result":{"video_info":{"accept_quality":[120,80],"dash":{"duration":1440,"video":[{"id":120,"codecid":12,"base_url":"https://video.test/4k"},{"id":80,"codecid":7,"base_url":"https://video.test/1080"}],"audio":[{"id":30280,"base_url":"https://audio.test/aac"}]}}}}`, func(r *http.Request) {
		if !strings.Contains(r.Header.Get("Cookie"), "SESSDATA=test-member-session") {
			t.Error("account session was not forwarded to PGC player")
		}
	})
	client.cookieData = &CookieData{SessData: "test-member-session"}
	sel, err := client.FetchStreamSelection(context.Background(), "BVtest", 1, 2, 42, true, "highest", "HEVC")
	if err != nil || sel.QualityID != 120 || sel.Codec != "HEVC" || sel.AudioURL == "" {
		t.Fatalf("member stream selection: %+v %v", sel, err)
	}
	qualities, err := client.GetAvailableQualities(context.Background(), "BVtest", 1, 2, 42, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range qualities {
		if q.IsAvailable && q.IsVipRequired {
			t.Fatalf("playable stream incorrectly marked as requiring membership: %+v", q)
		}
	}
}
