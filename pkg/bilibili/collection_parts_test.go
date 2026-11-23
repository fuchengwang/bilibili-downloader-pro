package bilibili

import (
	"context"
	"fmt"
	"testing"
)

func TestCollectionDetailReusesCompleteEmbeddedPages(t *testing.T) {
	client := playurlClient(t, `{"code":0,"data":{"bvid":"BVvalid","aid":1,"ugc_season":{"id":3,"ep_count":4,"sections":[{"episodes":[
		{"bvid":"BVvalid","aid":1,"cid":101,"title":"公开视频","arc":{"aid":1,"duration":30,"videos":0,"is_chargeable_season":false},"pages":[{"cid":101,"page":1,"part":"第一段","duration":12},{"cid":102,"page":2,"part":"第二段","duration":18}]},
		{"bvid":"BVpartial","aid":2,"cid":201,"arc":{"aid":2,"duration":30,"is_chargeable_season":false},"pages":[{"cid":201,"page":1,"duration":12}]},
		{"bvid":"BVwrong","aid":3,"cid":301,"arc":{"aid":3,"duration":12},"pages":[{"cid":302,"page":1,"duration":12}]},
		{"bvid":"BVpaid","aid":4,"cid":401,"arc":{"aid":4,"duration":12,"is_chargeable_season":true},"pages":[{"cid":401,"page":1,"duration":12}]}
	]}]}}}`, nil)
	detail, err := client.FetchVideoDetail(context.Background(), &ParsedTarget{Type: TargetNormal, BVID: "BVvalid", Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Episodes) != 4 || len(detail.CollectionVideos) != 2 {
		t.Fatalf("embedded metadata accepted or rejected unexpectedly: %+v", detail.CollectionVideos)
	}
	parts := detail.CollectionVideos["BVvalid"].Parts
	if len(parts) != 2 || parts[1].CID != 102 || parts[1].Title != "第二段" || parts[1].PartCount != 2 || parts[1].RequiresAccessCheck {
		t.Fatalf("multipart details lost: %+v", parts)
	}
	if !detail.CollectionVideos["BVpaid"].Parts[0].RequiresAccessCheck {
		t.Fatal("paid part lost its permission check")
	}
}

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
