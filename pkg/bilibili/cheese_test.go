package bilibili

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseCheeseInputKeepsCourseNamespace(t *testing.T) {
	c := &Client{}
	for _, tc := range []struct{ input, ep, ss string }{
		{"https://www.bilibili.com/cheese/play/ep2210114?csource=common_channelclass_watchedrecord_null", "2210114", ""},
		{"https://www.bilibili.com/cheese/play/ss802763862", "", "802763862"},
		{"https://m.bilibili.com/cheese/play?ep_id=2210114", "2210114", ""},
		{"https://www.bilibili.com/cheese/play?season_id=802763862", "", "802763862"},
		{"分享课堂：https://www.bilibili.com/cheese/play/ep2210114 来一起学习", "2210114", ""},
		{"cheese/ep2210114", "2210114", ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			target, err := c.ParseInput(context.Background(), tc.input)
			if err != nil || target.Type != TargetCheese || target.EPID != tc.ep || target.SSID != tc.ss {
				t.Fatalf("classroom input routed to another namespace: %+v %v", target, err)
			}
		})
	}
	if _, err := c.ParseInput(context.Background(), "https://www.bilibili.com/cheese/play/"); err == nil {
		t.Fatal("invalid course link accepted")
	}
	for _, input := range []string{"ep2210114", "https://www.bilibili.com/bangumi/play/ep2210114"} {
		got, err := c.ParseInput(context.Background(), input)
		if err != nil || got.Type != TargetBangumi {
			t.Fatalf("existing bangumi inputs changed: %+v %v", got, err)
		}
	}
}

func TestCheeseMetadataUsesAccountAndSeconds(t *testing.T) {
	c := playurlClient(t, `{"code":0,"data":{"season_id":44,"title":"课程","cover":"https://cover.test/course","up_info":{"uname":"老师"},"user_status":{"payed":1},"episodes":[{"id":101,"aid":201,"cid":301,"index":1,"title":"介绍","duration":45,"status":1},{"id":102,"aid":202,"cid":302,"index":2,"title":"正式课","duration":772,"status":2}]}}`, func(r *http.Request) {
		if r.URL.Path != "/pugv/view/web/season" || r.URL.Query().Get("ep_id") != "102" {
			t.Errorf("wrong course API: %s", r.URL)
		}
		if r.Header.Get("Cookie") != "SESSDATA=purchased-test" {
			t.Error("purchase account not sent to course API")
		}
	})
	c.cookieData = &CookieData{SessData: "purchased-test"}
	d, err := c.FetchVideoDetail(context.Background(), &ParsedTarget{Type: TargetCheese, EPID: "102"})
	if err != nil || d.Type != TargetCheese || d.SeasonID != 44 || d.DefaultPage != 2 || d.TotalParts != 2 {
		t.Fatalf("course metadata lost: %+v %v", d, err)
	}
	if d.Episodes[1].Duration != 772 || d.Episodes[1].DurationStr != "12:52" || d.Episodes[1].Badge != "已购买" || d.Episodes[0].Badge != "免费" {
		t.Fatalf("course duration/rights lost: %+v", d.Episodes)
	}
}

func TestCheeseCatalogueFetchesAllPagesAndLocatesLinkedLesson(t *testing.T) {
	c := &Client{httpClient: &http.Client{Transport: playurlTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"code":0,"data":{"season_id":44,"title":"课程","ep_count":3,"episodes":[{"id":102,"cid":302,"index":2}]}}`
		if r.URL.Path == "/pugv/view/web/ep/list" {
			if r.URL.Query().Get("season_id") != "44" || r.URL.Query().Get("ps") != "100" {
				t.Error("invalid course pagination request")
			}
			switch r.URL.Query().Get("pn") {
			case "1":
				body = `{"code":0,"data":{"items":[{"id":101,"cid":301,"index":1},{"id":102,"cid":302,"index":2}],"page":{"next":true,"total":3}}}`
			case "2":
				body = `{"code":0,"data":{"items":[{"id":103,"cid":303,"index":3}],"page":{"next":false,"total":3}}}`
			default:
				t.Fatal("pagination did not stop")
			}
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}}
	d, err := c.FetchVideoDetail(context.Background(), &ParsedTarget{Type: TargetCheese, EPID: "102"})
	if err != nil || d.TotalParts != 3 || d.DefaultPage != 2 || d.Episodes[2].EPID != 103 {
		t.Fatalf("partial or misplaced course catalogue: %+v %v", d, err)
	}
}

func TestCheesePlayURLUsesPurchaseSessionNotBangumi(t *testing.T) {
	c := playurlClient(t, `{"code":0,"data":{"is_preview":0,"has_paid":true,"dash":{"duration":772,"video":[{"id":112,"codecid":7,"base_url":"https://video.test/full"}],"audio":[{"id":30280,"base_url":"https://audio.test/full"}]}}}`, func(r *http.Request) {
		if r.URL.Path != "/pugv/player/web/playurl" || r.URL.Query().Get("ep_id") != "102" || r.URL.Query().Get("qn") != "127" {
			t.Errorf("course routed to wrong player: %s", r.URL)
		}
		if r.Header.Get("Cookie") != "SESSDATA=purchased-test" {
			t.Error("purchase account not sent to course player")
		}
	})
	c.cookieData = &CookieData{SessData: "purchased-test"}
	sel, err := c.FetchStreamSelection(context.Background(), "", 202, 302, 102, false, "highest", "auto", true)
	if err != nil || sel.Duration != 772 || sel.QualityID != 112 || sel.VideoURL == "" || sel.AudioURL == "" {
		t.Fatalf("purchased course stream lost: %+v %v", sel, err)
	}
	qs, err := c.GetAvailableQualities(context.Background(), "", 202, 302, 102, false, true)
	if err != nil || len(qs) != 1 || !qs[0].IsAvailable || qs[0].IsVipRequired {
		t.Fatalf("course purchase mistaken for VIP requirement: %+v %v", qs, err)
	}
}

func TestCheesePlayerRejectsPreviewAndPermissionErrors(t *testing.T) {
	for _, body := range []string{
		`{"code":0,"data":{"is_preview":1,"dash":{"video":[{"id":80,"codecid":7,"base_url":"https://video.test/preview"}]}}}`,
		`{"code":-101,"message":"账号未登录"}`,
		`{"code":-10403,"message":"课程未购买"}`,
		`{"code":0,"data":{}}`,
	} {
		t.Run(fmt.Sprint(body), func(t *testing.T) {
			c := playurlClient(t, body, nil)
			if _, err := c.FetchStreamSelection(context.Background(), "", 202, 302, 102, false, "highest", "auto", true); err == nil || strings.Contains(err.Error(), "番剧") {
				t.Fatalf("course restrictions hidden or misclassified: %v", err)
			}
		})
	}
}
