package bilibili

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestLiveCheeseFreeLessonWithoutPurchase(t *testing.T) {
	epid, err := strconv.ParseInt(os.Getenv("BBDOWN_CHEESE_TEST_FREE_EP"), 10, 64)
	if err != nil || epid <= 0 || testing.Short() {
		t.Skip("set BBDOWN_CHEESE_TEST_FREE_EP for a live free-classroom authorization check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Separate anonymous client: never change the app's signed-in cookie data.
	client := &Client{httpClient: &http.Client{Timeout: 30 * time.Second}}
	selection, err := client.FetchStreamSelection(ctx, "", 0, 0, epid, false, "highest", "auto", true)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := client.ResolveCourseKeys(ctx, selection)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, key := range keys {
			clear(key)
		}
	}()
	if len(selection.VideoURLs) == 0 || selection.Duration <= 0 {
		t.Fatal("free course did not return a complete playable stream")
	}
	t.Logf("Free classroom lesson available without purchase/login: quality %d, duration %ds", selection.QualityID, selection.Duration)
	if paid, err := strconv.ParseInt(os.Getenv("BBDOWN_CHEESE_TEST_PAID_EP"), 10, 64); err == nil && paid > 0 {
		if _, err := client.FetchStreamSelection(ctx, "", 0, 0, paid, false, "highest", "auto", true); err == nil {
			t.Fatal("paid lesson unexpectedly returned a full anonymous stream")
		}
		t.Log("Paid classroom lesson without purchase was correctly rejected")
	}
}
