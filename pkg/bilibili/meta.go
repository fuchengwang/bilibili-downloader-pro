package bilibili

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"bilibili_downloader/pkg/utils"
)

// EpisodeInfo 单个分P或番剧单集详情
type EpisodeInfo struct {
	Index       int    `json:"index"`       // 序号 (1-indexed)
	CID         int64  `json:"cid"`         // 视频流核心 CID
	BVID        string `json:"bvid"`        // 稿件 BVID
	AID         int64  `json:"aid"`         // 稿件 AID
	EPID        int64  `json:"epid"`        // 番剧 EPID (若有)
	Title       string `json:"title"`       // 单集主标题 (如 "P1 简介" 或 "第1话")
	LongTitle   string `json:"longTitle"`   // 单集长标题
	Duration    int    `json:"duration"`    // 时长 (秒)
	DurationStr string `json:"durationStr"` // 格式化时长 (01:20:30)
	Cover       string `json:"cover"`       // 分集封面 (若有)
	Badge       string `json:"badge"`       // 标签 (如 "大会员", "会员抢先", "预告")
}

// VideoDetail 包含视频主体信息及全部分集列表
type VideoDetail struct {
	Type         TargetType    `json:"type"`         // normal / bangumi
	BVID         string        `json:"bvid"`         // 稿件 BVID
	AID          int64         `json:"aid"`          // 稿件 AID
	Title        string        `json:"title"`        // 主标题
	Cover        string        `json:"cover"`        // 主封面
	Description  string        `json:"description"`  // 简介
	Duration     int           `json:"duration"`     // 总时长或主视频时长 (秒)
	DurationStr  string        `json:"durationStr"`  // 格式化时长
	PubDate      int64         `json:"pubDate"`      // 发布时间戳
	OwnerName    string        `json:"ownerName"`    // UP 主昵称 / 出品方
	OwnerFace    string        `json:"ownerFace"`    // UP 主头像
	OwnerMid     int64         `json:"ownerMid"`     // UP 主 UID
	ViewCount    int64         `json:"viewCount"`    // 播放量
	LikeCount    int64         `json:"likeCount"`    // 点赞数
	DanmakuCount int64         `json:"danmakuCount"` // 弹幕数
	IsCollection bool          `json:"isCollection"` // 是否为多P视频/专栏合集/番剧
	TotalParts   int           `json:"totalParts"`   // 总集数
	Episodes     []EpisodeInfo `json:"episodes"`     // 分集列表
	DefaultPage  int           `json:"defaultPage"`  // 用户请求定位的分P
}

// ---- API 响应结构体 ----

type viewResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Bvid        string `json:"bvid"`
		Aid         int64  `json:"aid"`
		Title       string `json:"title"`
		Pic         string `json:"pic"`
		Desc        string `json:"desc"`
		Duration    int    `json:"duration"`
		Pubdate     int64  `json:"pubdate"`
		RedirectURL string `json:"redirect_url"`
		Owner       struct {
			Mid  int64  `json:"mid"`
			Name string `json:"name"`
			Face string `json:"face"`
		} `json:"owner"`
		Stat struct {
			View    int64 `json:"view"`
			Danmaku int64 `json:"danmaku"`
			Like    int64 `json:"like"`
		} `json:"stat"`
		Pages []struct {
			Cid      int64  `json:"cid"`
			Page     int    `json:"page"`
			Part     string `json:"part"`
			Duration int    `json:"duration"`
			FirstPic string `json:"first_frame"`
		} `json:"pages"`
		UgcSeason *struct {
			ID       int64  `json:"id"`
			Title    string `json:"title"`
			Cover    string `json:"cover"`
			EpCount  int    `json:"ep_count"`
			Sections []struct {
				Title    string `json:"title"`
				Episodes []struct {
					ID       int64  `json:"id"`
					Aid      int64  `json:"aid"`
					Cid      int64  `json:"cid"`
					Title    string `json:"title"`
					Bvid     string `json:"bvid"`
					Arc      struct {
						Pic      string `json:"pic"`
						Duration int    `json:"duration"`
					} `json:"arc"`
				} `json:"episodes"`
			} `json:"sections"`
		} `json:"ugc_season"`
	} `json:"data"`
}

type seasonResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Result  struct {
		SeasonID int64  `json:"season_id"`
		Title    string `json:"title"`
		Cover    string `json:"cover"`
		Evaluate string `json:"evaluate"`
		UpInfo   struct {
			Mid    int64  `json:"mid"`
			Uname  string `json:"uname"`
			Avatar string `json:"avatar"`
		} `json:"up_info"`
		Stat struct {
			Views    int64 `json:"views"`
			Danmakus int64 `json:"danmakus"`
			Likes    int64 `json:"likes"`
		} `json:"stat"`
		Episodes []struct {
			ID        int64  `json:"id"`
			Aid       int64  `json:"aid"`
			Bvid      string `json:"bvid"`
			Cid       int64  `json:"cid"`
			Title     string `json:"title"`
			LongTitle string `json:"long_title"`
			Badge     string `json:"badge"`
			Cover     string `json:"cover"`
			Duration  int    `json:"duration"`
		} `json:"episodes"`
		Section []struct {
			Title    string `json:"title"`
			Episodes []struct {
				ID        int64  `json:"id"`
				Aid       int64  `json:"aid"`
				Bvid      string `json:"bvid"`
				Cid       int64  `json:"cid"`
				Title     string `json:"title"`
				LongTitle string `json:"long_title"`
				Badge     string `json:"badge"`
				Cover     string `json:"cover"`
				Duration  int    `json:"duration"`
			} `json:"episodes"`
		} `json:"section"`
	} `json:"result"`
}

// FetchVideoDetail 统一获取视频或合集、番剧的详细元数据
func (c *Client) FetchVideoDetail(ctx context.Context, target *ParsedTarget) (*VideoDetail, error) {
	if target.Type == TargetBangumi || target.EPID != "" || target.SSID != "" {
		return c.fetchBangumiDetail(ctx, target)
	}
	return c.fetchNormalDetail(ctx, target)
}

func (c *Client) fetchNormalDetail(ctx context.Context, target *ParsedTarget) (*VideoDetail, error) {
	var apiURL string
	if target.BVID != "" {
		apiURL = "https://api.bilibili.com/x/web-interface/view?bvid=" + url.QueryEscape(target.BVID)
	} else if target.AID != "" {
		apiURL = "https://api.bilibili.com/x/web-interface/view?aid=" + url.QueryEscape(target.AID)
	} else {
		return nil, fmt.Errorf("缺少有效的 BVID 或 AID")
	}

	var resp viewResponse
	if err := c.GetJSON(ctx, apiURL, &resp); err != nil {
		return nil, fmt.Errorf("获取视频详情失败: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("B站接口返回错误 (code=%d): %s", resp.Code, resp.Message)
	}

	d := resp.Data
	// 如果是番剧重定向
	if strings.Contains(d.RedirectURL, "bangumi") {
		if m := reEP.FindStringSubmatch(d.RedirectURL); len(m) > 1 {
			target.Type = TargetBangumi
			target.EPID = m[1]
			return c.fetchBangumiDetail(ctx, target)
		}
	}

	detail := &VideoDetail{
		Type:         TargetNormal,
		BVID:         d.Bvid,
		AID:          d.Aid,
		Title:        d.Title,
		Cover:        d.Pic,
		Description:  d.Desc,
		Duration:     d.Duration,
		DurationStr:  utils.FormatDuration(d.Duration),
		PubDate:      d.Pubdate,
		OwnerName:    d.Owner.Name,
		OwnerFace:    d.Owner.Face,
		OwnerMid:     d.Owner.Mid,
		ViewCount:    d.Stat.View,
		LikeCount:    d.Stat.Like,
		DanmakuCount: d.Stat.Danmaku,
		DefaultPage:  target.Page,
	}

	var episodes []EpisodeInfo

	// 1. 优先检查是否属于 UGC 系列/合集 (如 UP主创建的合集视频列表)
	if d.UgcSeason != nil && len(d.UgcSeason.Sections) > 0 {
		detail.IsCollection = true
		idx := 1
		for _, sec := range d.UgcSeason.Sections {
			for _, ep := range sec.Episodes {
				title := ep.Title
				if title == "" {
					title = fmt.Sprintf("第%d集", idx)
				}
				episodes = append(episodes, EpisodeInfo{
					Index:       idx,
					CID:         ep.Cid,
					BVID:        ep.Bvid,
					AID:         ep.Aid,
					Title:       title,
					Duration:    ep.Arc.Duration,
					DurationStr: utils.FormatDuration(ep.Arc.Duration),
					Cover:       ep.Arc.Pic,
				})
				idx++
			}
		}
	} else if len(d.Pages) > 0 {
		// 2. 普通分 P 视频
		if len(d.Pages) > 1 {
			detail.IsCollection = true
		}
		for _, p := range d.Pages {
			partTitle := p.Part
			if partTitle == "" {
				partTitle = fmt.Sprintf("P%d", p.Page)
			}
			cover := p.FirstPic
			if cover == "" {
				cover = d.Pic
			}
			episodes = append(episodes, EpisodeInfo{
				Index:       p.Page,
				CID:         p.Cid,
				BVID:        d.Bvid,
				AID:         d.Aid,
				Title:       partTitle,
				Duration:    p.Duration,
				DurationStr: utils.FormatDuration(p.Duration),
				Cover:       cover,
			})
		}
	}

	detail.Episodes = episodes
	detail.TotalParts = len(episodes)
	return detail, nil
}

func (c *Client) fetchBangumiDetail(ctx context.Context, target *ParsedTarget) (*VideoDetail, error) {
	var apiURL string
	if target.EPID != "" {
		apiURL = "https://api.bilibili.com/pgc/view/web/season?ep_id=" + url.QueryEscape(target.EPID)
	} else if target.SSID != "" {
		apiURL = "https://api.bilibili.com/pgc/view/web/season?season_id=" + url.QueryEscape(target.SSID)
	} else {
		return nil, fmt.Errorf("缺少番剧 EPID 或 SSID")
	}

	var resp seasonResponse
	if err := c.GetJSON(ctx, apiURL, &resp); err != nil {
		return nil, fmt.Errorf("获取番剧详情失败: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("番剧接口返回错误 (code=%d): %s", resp.Code, resp.Message)
	}

	res := resp.Result
	ownerName := res.UpInfo.Uname
	if ownerName == "" {
		ownerName = "哔哩哔哩番剧"
	}

	detail := &VideoDetail{
		Type:         TargetBangumi,
		Title:        res.Title,
		Cover:        res.Cover,
		Description:  res.Evaluate,
		OwnerName:    ownerName,
		OwnerFace:    res.UpInfo.Avatar,
		OwnerMid:     res.UpInfo.Mid,
		ViewCount:    res.Stat.Views,
		LikeCount:    res.Stat.Likes,
		DanmakuCount: res.Stat.Danmakus,
		IsCollection: true,
		DefaultPage:  target.Page,
	}

	var episodes []EpisodeInfo
	idx := 1

	// 正片集数
	for _, ep := range res.Episodes {
		title := ep.Title
		if ep.LongTitle != "" {
			title = title + " " + ep.LongTitle
		}
		if title == "" {
			title = fmt.Sprintf("第%d话", idx)
		}
		cover := ep.Cover
		if cover == "" {
			cover = res.Cover
		}
		episodes = append(episodes, EpisodeInfo{
			Index:       idx,
			CID:         ep.Cid,
			BVID:        ep.Bvid,
			AID:         ep.Aid,
			EPID:        ep.ID,
			Title:       title,
			LongTitle:   ep.LongTitle,
			Duration:    ep.Duration,
			DurationStr: utils.FormatDuration(ep.Duration),
			Cover:       cover,
			Badge:       ep.Badge,
		})
		idx++
	}

	// 番外/预告/花絮
	for _, sec := range res.Section {
		for _, ep := range sec.Episodes {
			title := ep.Title
			if ep.LongTitle != "" {
				title = title + " " + ep.LongTitle
			}
			if sec.Title != "" {
				title = "[" + sec.Title + "] " + title
			}
			cover := ep.Cover
			if cover == "" {
				cover = res.Cover
			}
			episodes = append(episodes, EpisodeInfo{
				Index:       idx,
				CID:         ep.Cid,
				BVID:        ep.Bvid,
				AID:         ep.Aid,
				EPID:        ep.ID,
				Title:       title,
				LongTitle:   ep.LongTitle,
				Duration:    ep.Duration,
				DurationStr: utils.FormatDuration(ep.Duration),
				Cover:       cover,
				Badge:       ep.Badge,
			})
			idx++
		}
	}

	if len(episodes) > 0 {
		detail.BVID = episodes[0].BVID
		detail.AID = episodes[0].AID
		detail.Duration = episodes[0].Duration
		detail.DurationStr = utils.FormatDuration(episodes[0].Duration)
	}

	// 若匹配特定 epid，设定 defaultPage
	if target.EPID != "" {
		for i, ep := range episodes {
			if strconv.FormatInt(ep.EPID, 10) == target.EPID {
				detail.DefaultPage = i + 1
				break
			}
		}
	}

	detail.Episodes = episodes
	detail.TotalParts = len(episodes)
	return detail, nil
}
