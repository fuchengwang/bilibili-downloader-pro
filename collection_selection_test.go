package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

func TestCollectionSelectionBoundedConcurrencyAndOrder(t *testing.T) {
	var entries []bilibili.EpisodeInfo
	var ids []string
	var cids []int64
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("BV%d", i)
		entries = append(entries, bilibili.EpisodeInfo{BVID: id, Index: i, CID: int64(i)})
		ids = append(ids, id)
		cids = append(cids, int64(i))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := make(chan struct{}, 12)
	release := make(chan struct{})
	var active, peak atomic.Int32
	done := make(chan struct{})
	var result []bilibili.EpisodeInfo
	var resultErr error
	go func() {
		defer close(done)
		result, resultErr = collectionSelection(ctx, entries, ids, cids, func(ctx context.Context, ep bilibili.EpisodeInfo) (*bilibili.CollectionVideo, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &bilibili.CollectionVideo{BVID: ep.BVID, Parts: []bilibili.EpisodeInfo{ep}}, nil
		})
	}()
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("requests were serialized or blocked")
		}
	}
	close(release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("validation did not finish")
	}
	if resultErr != nil || len(result) != 12 || peak.Load() != 4 {
		t.Fatalf("result=%v err=%v peak=%d", result, resultErr, peak.Load())
	}
	for i, ep := range result {
		if ep.Index != i+1 {
			t.Fatal("concurrency changed collection order")
		}
	}
}

func TestCollectionSelectionIgnoresUnselectedFailureAndRejectsStaleCID(t *testing.T) {
	entries := []bilibili.EpisodeInfo{{BVID: "BVok", CID: 11}, {BVID: "BVgone", CID: 21}}
	calls := 0
	fetch := func(_ context.Context, ep bilibili.EpisodeInfo) (*bilibili.CollectionVideo, error) {
		calls++
		if ep.BVID != "BVok" {
			return nil, errors.New("deleted")
		}
		return &bilibili.CollectionVideo{BVID: ep.BVID, Parts: []bilibili.EpisodeInfo{{CID: 11, Page: 1}, {CID: 12, Page: 2}}}, nil
	}
	parts, err := collectionSelection(context.Background(), entries, []string{"BVok"}, []int64{12}, fetch)
	if err != nil || len(parts) != 1 || parts[0].Page != 2 || calls != 1 {
		t.Fatalf("parts=%v err=%v calls=%d", parts, err, calls)
	}
	if _, err := collectionSelection(context.Background(), entries, []string{"BVok"}, []int64{99}, fetch); err == nil {
		t.Fatal("stale CID accepted")
	}
	before := calls
	if _, err := collectionSelection(context.Background(), entries, []string{"BVunknown"}, []int64{11}, fetch); err == nil || calls != before {
		t.Fatal("unverified video was fetched")
	}
	parts, err = collectionSelection(context.Background(), entries, nil, []int64{11}, fetch)
	if err != nil || len(parts) != 1 || parts[0].CID != 11 {
		t.Fatal("direct single-part request expanded selection")
	}
}

func TestCollectionSelectionCancelsOutstandingValidationOnFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	entries := []bilibili.EpisodeInfo{{BVID: "BVgone", CID: 11}, {BVID: "BVslow", CID: 21}}
	cause := errors.New("deleted video")
	_, err := collectionSelection(ctx, entries, []string{"BVgone", "BVslow"}, []int64{11, 21}, func(ctx context.Context, ep bilibili.EpisodeInfo) (*bilibili.CollectionVideo, error) {
		if ep.BVID == "BVgone" {
			return nil, cause
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if !errors.Is(err, cause) || ctx.Err() != nil {
		t.Fatalf("did not preserve failure and cancel pending work promptly: %v", err)
	}
}
