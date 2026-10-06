package bilibili

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"bilibili_downloader/pkg/utils"
)

type cheeseEpisode struct {
	ID          int64  `json:"id"`
	AID         int64  `json:"aid"`
	BVID        string `json:"bvid"`
	CID         int64  `json:"cid"`
	Index       int    `json:"index"`
	Title       string `json:"title"`
	Cover       string `json:"cover"`
	Duration    int    `json:"duration"` // PUGV uses seconds, unlike PGC.
	Status      int    `json:"status"`
	ReleaseDate int64  `json:"release_date"`
}

type cheesePage struct {
	Next  bool `json:"next"`
	Total int  `json:"total"`
}

type cheeseSeasonResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		SeasonID    int64           `json:"season_id"`
		Title       string          `json:"title"`
		Subtitle    string          `json:"subtitle"`
		Cover       string          `json:"cover"`
		EpCount     int             `json:"ep_count"`
		EpisodePage cheesePage      `json:"episode_page"`
		Episodes    []cheeseEpisode `json:"episodes"`
		UpInfo      struct {
			Mid    int64  `json:"mid"`
			Uname  string `json:"uname"`
			Avatar string `json:"avatar"`
		} `json:"up_info"`
		UserStatus struct {
			Payed     int  `json:"payed"`
			IsExpired bool `json:"is_expired"`
		} `json:"user_status"`
	} `json:"data"`
}

// fetchCheeseDetail uses the signed-in account for both free and purchased
// lessons. Access to a full stream is decided by the course player API later.
func (c *Client) fetchCheeseDetail(ctx context.Context, target *ParsedTarget) (*VideoDetail, error) {
	params := url.Values{}
	if target.EPID != "" {
		params.Set("ep_id", target.EPID)
	} else if target.SSID != "" {
		params.Set("season_id", target.SSID)
	} else {
		return nil, fmt.Errorf("课堂链接缺少课时 ep 号或课程 ss 号")
	}
	var resp cheeseSeasonResponse
	if err := c.GetJSON(ctx, "https://api.bilibili.com/pugv/view/web/season?"+params.Encode(), &resp); err != nil {
		return nil, fmt.Errorf("获取课堂课程详情失败: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("课堂接口返回错误 (code=%d): %s", resp.Code, resp.Message)
	}
	d := resp.Data
	if d.SeasonID <= 0 {
		return nil, fmt.Errorf("课堂接口未返回有效课程信息")
	}
	episodes := d.Episodes
	// The season response can contain only one page, including a page around
	// the linked lesson. Fetch the whole catalogue before exposing batch selection.
	if d.EpisodePage.Next || d.EpisodePage.Total > len(episodes) || d.EpCount > len(episodes) {
		var err error
		episodes, err = c.fetchCheeseEpisodes(ctx, d.SeasonID)
		if err != nil {
			return nil, err
		}
	}
	detail := &VideoDetail{
		Type: TargetCheese, SeasonID: d.SeasonID, Title: strings.TrimSpace(d.Title),
		CollectionTitle: strings.TrimSpace(d.Title), HasLinkedEpisode: target.EPID != "",
		Cover: d.Cover, Description: d.Subtitle, OwnerName: d.UpInfo.Uname,
		OwnerFace: d.UpInfo.Avatar, OwnerMid: d.UpInfo.Mid, DefaultPage: 1,
	}
	seen := make(map[int64]bool)
	linkedFound := target.EPID == ""
	for _, ep := range episodes {
		if ep.ID <= 0 || ep.CID <= 0 || seen[ep.ID] {
			continue // Unreleased lessons have no downloadable CID yet.
		}
		seen[ep.ID] = true
		index := ep.Index
		if index <= 0 {
			index = len(detail.Episodes) + 1
		}
		title := strings.TrimSpace(ep.Title)
		if title == "" {
			title = fmt.Sprintf("第%d课", index)
		}
		cover := ep.Cover
		if cover == "" {
			cover = d.Cover
		}
		badge := "付费"
		if ep.Status == 1 {
			badge = "免费"
		} else if d.UserStatus.Payed == 1 && !d.UserStatus.IsExpired {
			badge = "已购买"
		}
		detail.Episodes = append(detail.Episodes, EpisodeInfo{
			Index: index, CID: ep.CID, AID: ep.AID, BVID: ep.BVID, EPID: ep.ID,
			Title: title, Cover: cover, Duration: ep.Duration,
			DurationStr: utils.FormatDuration(ep.Duration), Badge: badge,
		})
		detail.Duration += ep.Duration
		if detail.PubDate == 0 || (ep.ReleaseDate > 0 && ep.ReleaseDate < detail.PubDate) {
			detail.PubDate = ep.ReleaseDate
		}
		if strconv.FormatInt(ep.ID, 10) == target.EPID {
			detail.DefaultPage = len(detail.Episodes)
			linkedFound = true
		}
	}
	if len(detail.Episodes) == 0 {
		return nil, fmt.Errorf("该课堂课程暂无已发布的视频课时")
	}
	if !linkedFound {
		return nil, fmt.Errorf("链接中的课堂课时尚未发布或不属于当前课程")
	}
	detail.TotalParts = len(detail.Episodes)
	detail.IsCollection = detail.TotalParts > 1
	detail.DurationStr = utils.FormatDuration(detail.Duration)
	return detail, nil
}

func (c *Client) fetchCheeseEpisodes(ctx context.Context, seasonID int64) ([]cheeseEpisode, error) {
	var episodes []cheeseEpisode
	seen := make(map[int64]bool)
	for page := 1; page <= 1000; page++ {
		params := url.Values{"season_id": {strconv.FormatInt(seasonID, 10)}, "pn": {strconv.Itoa(page)}, "ps": {"100"}}
		var resp struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Items []cheeseEpisode `json:"items"`
				Page  cheesePage      `json:"page"`
			} `json:"data"`
		}
		if err := c.GetJSON(ctx, "https://api.bilibili.com/pugv/view/web/ep/list?"+params.Encode(), &resp); err != nil {
			return nil, fmt.Errorf("获取课堂课时列表失败: %w", err)
		}
		if resp.Code != 0 {
			return nil, fmt.Errorf("课堂课时列表返回错误 (code=%d): %s", resp.Code, resp.Message)
		}
		previous := len(episodes)
		for _, ep := range resp.Data.Items {
			if ep.ID > 0 && !seen[ep.ID] {
				seen[ep.ID] = true
				episodes = append(episodes, ep)
			}
		}
		if !resp.Data.Page.Next {
			return episodes, nil
		}
		if len(episodes) == previous {
			return nil, fmt.Errorf("课堂课时列表分页未返回新课时，请稍后重试")
		}
	}
	return nil, fmt.Errorf("课堂课时列表页数过多，无法获取完整目录")
}

func (c *Client) requestCheesePlayURL(ctx context.Context, aid, cid, epid int64) (*playurlAPIResp, error) {
	if epid <= 0 {
		return nil, fmt.Errorf("课堂下载缺少有效的课时 ep 号")
	}
	params := url.Values{
		"ep_id": {strconv.FormatInt(epid, 10)}, "qn": {"127"},
		"fnval": {"4048"}, "fnver": {"0"}, "fourk": {"1"}, "otype": {"json"},
	}
	if aid > 0 {
		params.Set("avid", strconv.FormatInt(aid, 10))
	}
	if cid > 0 {
		params.Set("cid", strconv.FormatInt(cid, 10))
	}
	var resp playurlAPIResp
	if err := c.GetJSON(ctx, "https://api.bilibili.com/pugv/player/web/playurl?"+params.Encode(), &resp); err != nil {
		return nil, fmt.Errorf("请求课堂媒体流失败: %w", err)
	}
	if resp.Code != 0 {
		return nil, playbackAPIError(resp.Code, resp.Message)
	}
	if resp.isPreview() {
		err := playbackError("preview_only", "课堂接口仅返回试看内容")
		err.Hint = "请登录已购买该课程且课程未过期的账号后重试。"
		return nil, err
	}
	if resp.getDash() == nil {
		return nil, playbackError("unavailable", "课堂接口未返回可下载的视频流")
	}
	return &resp, nil
}
