package bilibili

import (
	"context"
	"fmt"
	"testing"
)

func TestFetchCollectionVideoPartsPreservesBothLevels(t *testing.T) {
	c := playurlClient(t, `{"code":0,"data":{"bvid":"BVexample","aid":20,"pages":[{"cid":200,"page":1,"part":"入门"},{"cid":201,"page":2,"part":"练习"}],"ugc_season":{"title":"合集"}}}`, nil)
	v, err := c.FetchCollectionVideoParts(context.Background(), EpisodeInfo{Index: 3, BVID: "BVexample", Title: "第一册"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Index != 3 || v.Title != "第一册" || len(v.Parts) != 2 || v.Parts[1].Index != 3 || v.Parts[1].Page != 2 || v.Parts[1].CID != 201 {
		t.Fatalf("nested collection was not preserved: %+v", v)
	}
}

func TestFetchCollectionVideoPartsRejectsDuplicateOrInvalidIdentity(t *testing.T) {
	for _, pages := range []string{
		`[{"cid":200,"page":1},{"cid":200,"page":2}]`,
		`[{"cid":200,"page":1},{"cid":201,"page":1}]`,
		`[{"cid":0,"page":1}]`,
	} {
		body := fmt.Sprintf(`{"code":0,"data":{"bvid":"BVexample","aid":20,"pages":%s}}`, pages)
		c := playurlClient(t, body, nil)
		if _, err := c.FetchCollectionVideoParts(context.Background(), EpisodeInfo{BVID: "BVexample"}); err == nil {
			t.Fatalf("invalid pages accepted: %s", pages)
		}
	}
}

func TestFetchCollectionVideoPartsRejectsIncompleteMetadata(t *testing.T) {
	c := playurlClient(t, `{"code":0,"data":{"bvid":"BVexample","pages":[]}}`, nil)
	if _, err := c.FetchCollectionVideoParts(context.Background(), EpisodeInfo{BVID: "BVexample"}); err == nil {
		t.Fatal("missing pages must not be treated as a complete video")
	}
}
