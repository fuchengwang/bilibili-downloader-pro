package bilibili

import (
	"context"
	"os"
	"testing"
	"time"
)

// Opt-in comparison using the app's saved session. Never logs cookies or media URLs.
func TestLiveChargeAccessComparison(t *testing.T) {
	denied, granted := os.Getenv("BBDOWN_CHARGE_DENIED_BVID"), os.Getenv("BBDOWN_CHARGE_GRANTED_BVID")
	if testing.Short() || denied == "" || granted == "" {
		t.Skip("opt-in charged and uncharged video comparison")
	}
	c := GetDefaultClient()
	if !c.IsLoggedIn() {
		t.Fatal("no saved B站 login session available for the charged video comparison")
	}
	for _, tc := range []struct {
		bvid    string
		granted bool
	}{{denied, false}, {granted, true}} {
		t.Run(tc.bvid, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			detail, err := c.FetchVideoDetail(ctx, &ParsedTarget{Type: TargetNormal, BVID: tc.bvid, Page: 1})
			if err != nil {
				t.Fatal(err)
			}
			if !detail.HasLinkedEpisode || detail.DefaultPage < 1 || detail.DefaultPage > len(detail.Episodes) {
				t.Fatal("linked video was not located")
			}
			ep := detail.Episodes[detail.DefaultPage-1]
			info := c.GetPlaybackInfo(ctx, ep.BVID, ep.AID, ep.CID, ep.EPID, false, false)
			if tc.granted {
				if info.Error != nil {
					t.Fatalf("charged video incorrectly blocked: %s", info.Error.Error())
				}
				sel, err := c.FetchStreamSelection(ctx, ep.BVID, ep.AID, ep.CID, ep.EPID, false, "highest", "auto")
				if err != nil {
					t.Fatal(err)
				}
				if sel.VideoURL == "" || sel.AudioURL == "" || sel.Duration <= 0 {
					t.Fatal("charged video has no complete audio/video selection")
				}
				t.Logf("charged video accepted: quality=%d duration=%ds", sel.QualityID, sel.Duration)
			} else {
				if info.Error == nil || info.Error.Kind != "charge_required" {
					t.Fatalf("uncharged video not identified: %+v", info.Error)
				}
				t.Logf("uncharged video correctly reported: %s", info.Error.Message)
			}
		})
	}
}
