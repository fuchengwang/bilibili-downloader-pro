package bilibili

import (
	"context"
	"os"
	"testing"
	"time"
)

// Opt-in read-only check. Logs quality IDs only, never cookies or signed URLs.
func TestLiveHighestQuality(t *testing.T) {
	bvid := os.Getenv("BBDOWN_QUALITY_TEST_BVID")
	if bvid == "" || testing.Short() {
		t.Skip("opt-in live quality check")
	}
	c := GetDefaultClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	detail, err := c.FetchVideoDetail(ctx, &ParsedTarget{Type: TargetNormal, BVID: bvid})
	if err != nil {
		t.Fatal(err)
	}
	if detail.DefaultPage < 1 || detail.DefaultPage > len(detail.Episodes) || detail.Episodes[detail.DefaultPage-1].BVID != bvid {
		t.Fatalf("quality picker does not target the linked video: page=%d", detail.DefaultPage)
	}
	for _, ep := range detail.Episodes {
		if ep.Index == 1 {
			qs, err := c.GetAvailableQualities(ctx, ep.BVID, ep.AID, ep.CID, 0, false)
			t.Logf("collection first bvid=%s cid=%d options=%+v err=%v defaultPage=%d", ep.BVID, ep.CID, qs, err, detail.DefaultPage)
			first, err := c.FetchStreamSelection(ctx, ep.BVID, ep.AID, ep.CID, 0, false, "highest", "auto")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("collection first highest=%d dimensions=%dx%d", first.QualityID, first.Width, first.Height)
		}
		if ep.BVID != bvid {
			continue
		}
		qs, err := c.GetAvailableQualities(ctx, ep.BVID, ep.AID, ep.CID, 0, false)
		if err != nil {
			t.Fatal(err)
		}
		sel, err := c.FetchStreamSelection(ctx, ep.BVID, ep.AID, ep.CID, 0, false, "highest", "auto")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("bvid=%s cid=%d loggedIn=%t options=%+v selected=%d dimensions=%dx%d", ep.BVID, ep.CID, c.IsLoggedIn(), qs, sel.QualityID, sel.Width, sel.Height)
		for _, q := range qs {
			if q.IsAvailable && q.ID > sel.QualityID {
				t.Fatalf("highest selected %d despite available quality %d", sel.QualityID, q.ID)
			}
		}
		return
	}
	t.Fatal("requested video missing from collection")
}
