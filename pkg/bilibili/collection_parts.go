package bilibili

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"bilibili_downloader/pkg/utils"
)

// CollectionVideo preserves the two independent levels of a UGC collection.
type CollectionVideo struct {
	Index int           `json:"index"`
	Title string        `json:"title"`
	BVID  string        `json:"bvid"`
	Parts []EpisodeInfo `json:"parts"`
}

// Share the budget between background browsing and submission validation.
var collectionPartSlots = make(chan struct{}, 4)

// FetchCollectionVideoParts retrieves a video's pages even when it belongs to
// a UGC collection. FetchVideoDetail deliberately prioritizes the collection.
func (c *Client) FetchCollectionVideoParts(ctx context.Context, video EpisodeInfo) (*CollectionVideo, error) {
	if video.BVID == "" {
		return nil, fmt.Errorf("合集视频缺少 BV 号")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case collectionPartSlots <- struct{}{}:
		defer func() { <-collectionPartSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var resp viewResponse
	if err := c.GetJSON(ctx, "https://api.bilibili.com/x/web-interface/view?bvid="+url.QueryEscape(video.BVID), &resp); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("获取视频分P失败 (code=%d): %s", resp.Code, resp.Message)
	}
	if resp.Data.Bvid != video.BVID || resp.Data.Aid <= 0 || len(resp.Data.Pages) == 0 {
		return nil, fmt.Errorf("视频 %s 的分P信息不完整", video.BVID)
	}
	videoTitle := video.VideoTitle
	if videoTitle == "" {
		videoTitle = video.Title
	}
	result := &CollectionVideo{Index: video.Index, Title: videoTitle, BVID: video.BVID}
	seenCID, seenPage := map[int64]bool{}, map[int]bool{}
	for _, p := range resp.Data.Pages {
		if p.Cid <= 0 || p.Page <= 0 || seenCID[p.Cid] || seenPage[p.Page] {
			return nil, fmt.Errorf("视频 %s 的分P信息不完整", video.BVID)
		}
		seenCID[p.Cid], seenPage[p.Page] = true, true
		cover := p.FirstPic
		if cover == "" {
			cover = video.Cover
		}
		title := p.Part
		if title == "" {
			title = fmt.Sprintf("P%d", p.Page)
		}
		result.Parts = append(result.Parts, EpisodeInfo{Index: video.Index, Page: p.Page, CID: p.Cid, BVID: video.BVID, AID: resp.Data.Aid, Title: title, Duration: p.Duration, DurationStr: utils.FormatDuration(p.Duration), Cover: cover, VideoTitle: videoTitle, PartCount: len(resp.Data.Pages)})
	}
	return result, nil
}
