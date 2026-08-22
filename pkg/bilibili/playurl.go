package bilibili

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var QualityMap = map[int]string{
	127: "8K 超高清",
	126: "杜比视界",
	125: "HDR 真彩",
	120: "4K 超清",
	116: "1080P 60帧",
	112: "1080P 高码率",
	100: "智能修复",
	80:  "1080P 高清",
	74:  "720P 60帧",
	64:  "720P 高清",
	32:  "480P 清晰",
	16:  "360P 流畅",
	6:   "240P 流畅",
}

// QualityOption 前端下拉选择的清晰度选项
type QualityOption struct {
	ID              int    `json:"id"`
	Label           string `json:"label"`
	Codecs          string `json:"codecs"`
	IsVipRequired   bool   `json:"isVipRequired"`
	IsLoginRequired bool   `json:"isLoginRequired"`
	IsAvailable     bool   `json:"isAvailable"`
}

// StreamSelection 选定的视频与音频下载直链信息
type StreamSelection struct {
	VideoURL     string `json:"videoUrl"`
	AudioURL     string `json:"audioUrl"`
	QualityID    int    `json:"qualityId"`
	QualityLabel string `json:"qualityLabel"`
	Codec        string `json:"codec"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Duration     int    `json:"duration"`
	VideoSize    int64  `json:"videoSize"`
	AudioSize    int64  `json:"audioSize"`
}

type DashStream struct {
	ID        int      `json:"id"`
	BaseURL   string   `json:"base_url"`
	BackupURL []string `json:"backup_url"`
	Bandwidth int64    `json:"bandwidth"`
	Codecid   int      `json:"codecid"`
	Codecs    string   `json:"codecs"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	FrameRate string   `json:"frame_rate"`
}

type DashData struct {
	Duration int          `json:"duration"`
	Video    []DashStream `json:"video"`
	Audio    []DashStream `json:"audio"`
	Dolby    struct {
		Audio []DashStream `json:"audio"`
	} `json:"dolby"`
	Flac *struct {
		Audio *DashStream `json:"audio"`
	} `json:"flac"`
}

type playurlAPIResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    *struct {
		Dash               *DashData `json:"dash"`
		AcceptDescription  []string  `json:"accept_description"`
		AcceptQuality      []int     `json:"accept_quality"`
		SupportFormats     []struct {
			Quality        int      `json:"quality"`
			Format         string   `json:"format"`
			NewDescription string   `json:"new_description"`
			DisplayDesc    string   `json:"display_desc"`
			Codecs         []string `json:"codecs"`
		} `json:"support_formats"`
	} `json:"data"`
	Result *struct {
		Dash               *DashData `json:"dash"`
		AcceptDescription  []string  `json:"accept_description"`
		AcceptQuality      []int     `json:"accept_quality"`
		SupportFormats     []struct {
			Quality        int      `json:"quality"`
			Format         string   `json:"format"`
			NewDescription string   `json:"new_description"`
			DisplayDesc    string   `json:"display_desc"`
			Codecs         []string `json:"codecs"`
		} `json:"support_formats"`
	} `json:"result"`
}

func (r *playurlAPIResp) getDash() *DashData {
	if r.Data != nil && r.Data.Dash != nil {
		return r.Data.Dash
	}
	if r.Result != nil && r.Result.Dash != nil {
		return r.Result.Dash
	}
	return nil
}

func (r *playurlAPIResp) getAcceptQualities() ([]int, []string) {
	if r.Data != nil && len(r.Data.AcceptQuality) > 0 {
		return r.Data.AcceptQuality, r.Data.AcceptDescription
	}
	if r.Result != nil && len(r.Result.AcceptQuality) > 0 {
		return r.Result.AcceptQuality, r.Result.AcceptDescription
	}
	return nil, nil
}

// GetAvailableQualities 获取当前分P在当前登录状态下所有可选的清晰度列表
func (c *Client) GetAvailableQualities(ctx context.Context, bvid string, aid, cid, epid int64, isBangumi bool) ([]QualityOption, error) {
	resp, err := c.requestPlayURL(ctx, bvid, aid, cid, epid, isBangumi)
	if err != nil {
		return nil, err
	}

	qids, qnames := resp.getAcceptQualities()
	dash := resp.getDash()

	// 收集 DASH 实际已返回的画质 ID
	dashQids := make(map[int]bool)
	if dash != nil {
		for _, v := range dash.Video {
			dashQids[v.ID] = true
		}
	}

	var options []QualityOption
	for i, qn := range qids {
		label := ""
		if i < len(qnames) {
			label = qnames[i]
		}
		if label == "" {
			if l, ok := QualityMap[qn]; ok {
				label = l
			} else {
				label = fmt.Sprintf("%dP", qn)
			}
		}

		isVip := qn >= 112 || qn == 74 || qn == 126 || qn == 125 // 8K, 4K, 1080P60, 1080P+, 720P60, 杜比, HDR 需要大会员
		isLogin := qn >= 64                                      // 1080P, 720P 均需要登录

		options = append(options, QualityOption{
			ID:              qn,
			Label:           label,
			IsVipRequired:   isVip,
			IsLoginRequired: isLogin,
			IsAvailable:     dashQids[qn],
		})
	}

	return options, nil
}

// FetchStreamSelection 根据指定清晰度偏好与编码偏好获取视频和音频下载直链
func (c *Client) FetchStreamSelection(ctx context.Context, bvid string, aid, cid, epid int64, isBangumi bool, targetQuality string, targetCodec string) (*StreamSelection, error) {
	resp, err := c.requestPlayURL(ctx, bvid, aid, cid, epid, isBangumi)
	if err != nil {
		return nil, err
	}

	dash := resp.getDash()
	if dash == nil || len(dash.Video) == 0 {
		return nil, fmt.Errorf("B站未返回 DASH 媒体流（请确认是否需登录或账号权限）")
	}

	video := pickVideoStream(dash.Video, targetQuality, targetCodec)
	audio := pickAudioStream(dash)

	if video == nil {
		return nil, fmt.Errorf("未找到满足条件的可用视频轨")
	}

	videoURL := chooseBestCDN(video.BaseURL, video.BackupURL)
	audioURL := ""
	if audio != nil {
		audioURL = chooseBestCDN(audio.BaseURL, audio.BackupURL)
	}

	label, ok := QualityMap[video.ID]
	if !ok {
		label = fmt.Sprintf("%dP", video.ID)
	}

	codecName := "AVC"
	switch video.Codecid {
	case 13:
		codecName = "AV1"
	case 12:
		codecName = "HEVC"
	case 7:
		codecName = "AVC"
	}

	return &StreamSelection{
		VideoURL:     videoURL,
		AudioURL:     audioURL,
		QualityID:    video.ID,
		QualityLabel: label,
		Codec:        codecName,
		Width:        video.Width,
		Height:       video.Height,
		Duration:     dash.Duration,
	}, nil
}

// requestPlayURL 具备多层降级策略的媒体流请求函数
func (c *Client) requestPlayURL(ctx context.Context, bvid string, aid, cid, epid int64, isBangumi bool) (*playurlAPIResp, error) {
	// 1. 番剧使用 pgc/player API
	if isBangumi {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		params := url.Values{}
		params.Set("support_multi_audio", "true")
		params.Set("from_client", "BROWSER")
		if aid > 0 {
			params.Set("avid", strconv.FormatInt(aid, 10))
		}
		params.Set("cid", strconv.FormatInt(cid, 10))
		if epid > 0 {
			params.Set("ep_id", strconv.FormatInt(epid, 10))
		}
		params.Set("fnval", "4048")
		params.Set("fnver", "0")
		params.Set("fourk", "1")
		params.Set("otype", "json")
		params.Set("module", "bangumi")
		params.Set("wts", ts)
		if !c.IsLoggedIn() {
			params.Set("try_look", "1")
		}
		reqURL := "https://api.bilibili.com/pgc/player/web/v2/playurl?" + params.Encode()
		var resp playurlAPIResp
		if err := c.GetJSON(ctx, reqURL, &resp); err == nil && resp.Code == 0 && resp.getDash() != nil {
			return &resp, nil
		}
	}

	// 2. 普通视频：优先尝试标准 playurl 官方 API (轻量极速，免风控阻断)
	stdURL := fmt.Sprintf("https://api.bilibili.com/x/player/playurl?bvid=%s&avid=%d&cid=%d&qn=127&fnval=4048&fnver=0&fourk=1&otype=json",
		url.QueryEscape(bvid), aid, cid)
	var stdResp playurlAPIResp
	if err := c.GetJSON(ctx, stdURL, &stdResp); err == nil && stdResp.Code == 0 && stdResp.getDash() != nil {
		return &stdResp, nil
	}

	// 3. 次级降级：尝试 WBI 签名请求
	paramMap := map[string]string{
		"avid":                strconv.FormatInt(aid, 10),
		"bvid":                bvid,
		"cid":                 strconv.FormatInt(cid, 10),
		"fnval":               "4048",
		"fnver":               "0",
		"fourk":               "1",
		"from_client":         "BROWSER",
		"otype":               "json",
		"qn":                  "127",
		"support_multi_audio": "true",
	}
	if !c.IsLoggedIn() {
		paramMap["try_look"] = "1"
	}
	if signedQuery, err := c.SignWbiParams(ctx, paramMap); err == nil {
		wbiURL := "https://api.bilibili.com/x/player/wbi/playurl?" + signedQuery
		var wbiResp playurlAPIResp
		if err := c.GetJSON(ctx, wbiURL, &wbiResp); err == nil && wbiResp.Code == 0 && wbiResp.getDash() != nil {
			return &wbiResp, nil
		}
	}

	// 4. 三级降级：从 HTML 页面提取 window.__playinfo__
	if htmlResp, err := c.extractPlayInfoFromHTML(ctx, bvid); err == nil && htmlResp != nil && htmlResp.getDash() != nil {
		return htmlResp, nil
	}

	if stdResp.Code != 0 {
		return nil, fmt.Errorf("playurl 接口返回 (code=%d): %s", stdResp.Code, stdResp.Message)
	}

	return nil, fmt.Errorf("未能获取到可用的 DASH 音视频流")
}

// extractPlayInfoFromHTML 从网页 HTML 中直接提取 window.__playinfo__
func (c *Client) extractPlayInfoFromHTML(ctx context.Context, bvid string) (*playurlAPIResp, error) {
	pageURL := fmt.Sprintf("https://www.bilibili.com/video/%s", bvid)
	body, err := c.GetBytes(ctx, pageURL, nil)
	if err != nil {
		return nil, err
	}

	html := string(body)
	idx := strings.Index(html, "window.__playinfo__")
	if idx == -1 {
		return nil, fmt.Errorf("playinfo not found in HTML")
	}

	eq := strings.Index(html[idx:], "=")
	if eq == -1 {
		return nil, fmt.Errorf("playinfo delimiter not found")
	}
	start := strings.Index(html[idx+eq:], "{")
	if start == -1 {
		return nil, fmt.Errorf("playinfo json start not found")
	}

	absStart := idx + eq + start
	depth := 0
	absEnd := -1
	for i := absStart; i < len(html); i++ {
		if html[i] == '{' {
			depth++
		} else if html[i] == '}' {
			depth--
			if depth == 0 {
				absEnd = i + 1
				break
			}
		}
	}

	if absEnd == -1 {
		return nil, fmt.Errorf("playinfo json end not found")
	}

	jsonBytes := []byte(html[absStart:absEnd])
	var resp playurlAPIResp
	if err := json.Unmarshal(jsonBytes, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

func pickVideoStream(videos []DashStream, targetQuality string, targetCodec string) *DashStream {
	if len(videos) == 0 {
		return nil
	}

	targetQN := 0
	if targetQuality != "" && targetQuality != "highest" {
		if qn, err := strconv.Atoi(targetQuality); err == nil && qn > 0 {
			targetQN = qn
		}
	}

	// 1. 如果指定了画质限制
	var candidates []DashStream
	if targetQN > 0 {
		for _, v := range videos {
			if v.ID <= targetQN {
				candidates = append(candidates, v)
			}
		}
	}
	if len(candidates) == 0 {
		candidates = videos
	}

	// 2. 编码筛选 (如果用户指定了偏好且候选中有符合编码的)
	if targetCodec != "" && targetCodec != "auto" {
		var codecMatches []DashStream
		for _, v := range candidates {
			match := false
			switch strings.ToUpper(targetCodec) {
			case "AVC", "H.264", "H264":
				match = (v.Codecid == 7)
			case "HEVC", "H.265", "H265":
				match = (v.Codecid == 12)
			case "AV1":
				match = (v.Codecid == 13)
			}
			if match {
				codecMatches = append(codecMatches, v)
			}
		}
		if len(codecMatches) > 0 {
			candidates = codecMatches
		}
	}

	// 3. 排序：ID 最高 (清晰度最高) -> 带宽/码率最高
	var best *DashStream
	for i := range candidates {
		v := &candidates[i]
		if best == nil || v.ID > best.ID || (v.ID == best.ID && v.Bandwidth > best.Bandwidth) {
			best = v
		}
	}
	return best
}

func pickAudioStream(dash *DashData) *DashStream {
	var allAudio []DashStream
	allAudio = append(allAudio, dash.Audio...)
	allAudio = append(allAudio, dash.Dolby.Audio...)
	if dash.Flac != nil && dash.Flac.Audio != nil {
		allAudio = append(allAudio, *dash.Flac.Audio)
	}

	var best *DashStream
	for i := range allAudio {
		a := &allAudio[i]
		if a.BaseURL == "" {
			continue
		}
		if best == nil || a.Bandwidth > best.Bandwidth {
			best = a
		}
	}
	return best
}

var reExplicitPort = regexp.MustCompile(`https?://[^/]+:\d+`)

// chooseBestCDN 挑选不带特定端口号的 CDN 节点 (优先规避 PCDN 与限速节点)
func chooseBestCDN(baseURL string, backupURLs []string) string {
	candidates := append([]string{baseURL}, backupURLs...)
	for _, u := range candidates {
		if u != "" && !reExplicitPort.MatchString(u) {
			return u
		}
	}
	return baseURL
}
