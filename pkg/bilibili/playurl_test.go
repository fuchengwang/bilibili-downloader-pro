package bilibili

import (
	"context"
	"testing"
	"time"
)

func TestPlayURLResolve(t *testing.T) {
	client := GetDefaultClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	parsed, err := client.ParseInput(ctx, "BV1xx411c7mD")
	if err != nil {
		t.Fatalf("ParseInput failed: %v", err)
	}

	detail, err := client.FetchVideoDetail(ctx, parsed)
	if err != nil {
		t.Fatalf("FetchVideoDetail failed: %v", err)
	}

	ep := detail.Episodes[0]
	t.Logf("Testing CID=%d, AID=%d, BVID=%s", ep.CID, ep.AID, ep.BVID)

	qualities, err := client.GetAvailableQualities(ctx, ep.BVID, ep.AID, ep.CID, 0, false)
	if err != nil {
		t.Fatalf("GetAvailableQualities failed: %v", err)
	}
	t.Logf("Got %d qualities: %+v", len(qualities), qualities)

	sel, err := client.FetchStreamSelection(ctx, ep.BVID, ep.AID, ep.CID, 0, false, "highest", "auto")
	if err != nil {
		t.Fatalf("FetchStreamSelection failed: %v", err)
	}
	t.Logf("Stream URL: %s, QN: %d, Label: %s", sel.VideoURL, sel.QualityID, sel.QualityLabel)
}
