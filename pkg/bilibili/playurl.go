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
	VideoURL     string   `json:"videoUrl"`
	VideoURLs    []string `json:"videoUrls"`
	AudioURL     string   `json:"audioUrl"`
	AudioURLs    []string `json:"audioUrls"`
	QualityID    int      `json:"qualityId"`
	QualityLabel string   `json:"qualityLabel"`
	Codec        string   `json:"codec"`
	Width        int      `json:"width"`
	Height       int      `json:"height"`
	Duration     int      `json:"duration"`
	VideoSize    int64    `json:"videoSize"`
	AudioSize    int64    `json:"audioSize"`
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
		Dash              *DashData `json:"dash"`
		AcceptDescription []string  `json:"accept_description"`
		AcceptQuality     []int     `json:"accept_quality"`
		SupportFormats    []struct {
			Quality        int      `json:"quality"`
			Format         string   `json:"format"`
			NewDescription string   `json:"new_description"`
			DisplayDesc    string   `json:"display_desc"`
			Codecs         []string `json:"codecs"`
		} `json:"support_formats"`
	} `json:"data"`
	Result *struct {
		Dash              *DashData `json:"dash"`
		AcceptDescription []string  `json:"accept_description"`
		AcceptQuality     []int     `json:"accept_quality"`
		SupportFormats    []struct {
			Quality        int      `json:"quality"`
			Format         string   `json:"format"`
			NewDescription string   `json:"new_description"`
			DisplayDesc    string   `json:"display_desc"`
			Codecs         []string `json:"codecs"`
		} `json:"support_formats"`
		VideoInfo *struct {
			Dash              *DashData `json:"dash"`
			AcceptDescription []string  `json:"accept_description"`
			AcceptQuality     []int     `json:"accept_quality"`
			SupportFormats    []struct {
				Quality        int      `json:"quality"`
				Format         string   `json:"format"`
				NewDescription string   `json:"new_description"`
				DisplayDesc    string   `json:"display_desc"`
				Codecs         []string `json:"codecs"`
			} `json:"support_formats"`
		} `json:"video_info"`
	} `json:"result"`
}

func (r *playurlAPIResp) getDash() *DashData {
	if r == nil {
		return nil
	}
	if r.Data != nil && r.Data.Dash != nil {
		return r.Data.Dash
	}
	if r.Result != nil {
		if r.Result.Dash != nil {
			return r.Result.Dash
		}
		if r.Result.VideoInfo != nil && r.Result.VideoInfo.Dash != nil {
			return r.Result.VideoInfo.Dash
		}
	}
	return nil
}

func (r *playurlAPIResp) getAcceptQualities() ([]int, []string) {
	if r == nil {
		return nil, nil
	}
	if r.Data != nil && len(r.Data.AcceptQuality) > 0 {
		return r.Data.AcceptQuality, r.Data.AcceptDescription
	}
	if r.Result != nil {
		if len(r.Result.AcceptQuality) > 0 {
			return r.Result.AcceptQuality, r.Result.AcceptDescription
		}
		if r.Result.VideoInfo != nil && len(r.Result.VideoInfo.AcceptQuality) > 0 {
			return r.Result.VideoInfo.AcceptQuality, r.Result.VideoInfo.AcceptDescription
		}
	}
	return nil, nil
}

func getDefaultQualityOptions(isBangumi bool) []QualityOption {
	standards := []struct {
		id    int
		label string
		isVip bool
	}{
		{120, "4K 超清", true},
		{116, "1080P 60帧", true},
		{80, "1080P 高清", isBangumi},
		{64, "720P 高清", isBangumi},
		{32, "480P 清晰", isBangumi},
		{16, "360P 流畅", isBangumi},
	}

	var res []QualityOption
	for _, s := range standards {
		res = append(res, QualityOption{
			ID:              s.id,
			Label:           s.label,
			IsVipRequired:   s.isVip,
			IsLoginRequired: true,
			IsAvailable:     false,
		})
	}
	return res
}

// GetAvailableQualities 获取当前分P在当前登录状态下所有可选的清晰度列表
func (c *Client) GetAvailableQualities(ctx context.Context, bvid string, aid, cid, epid int64, isBangumi bool) ([]QualityOption, error) {
	resp, err := c.requestPlayURL(ctx, bvid, aid, cid, epid, isBangumi)
	if err != nil || resp == nil {
		return getDefaultQualityOptions(isBangumi), nil
	}

	qids, qnames := resp.getAcceptQualities()
	dash := resp.getDash()

	if len(qids) == 0 && dash == nil {
		return getDefaultQualityOptions(isBangumi), nil
	}

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
		if isBangumi && qn >= 80 {
			isVip = true
		}
		isLogin := qn >= 64 // 1080P, 720P 均需要登录

		options = append(options, QualityOption{
			ID:              qn,
			Label:           label,
			IsVipRequired:   isVip,
			IsLoginRequired: isLogin,
			IsAvailable:     dashQids[qn],
		})
	}

	if len(options) == 0 {
		return getDefaultQualityOptions(isBangumi), nil
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
		if isBangumi || epid > 0 {
			return nil, fmt.Errorf("该视频为哔哩哔哩大会员专享内容，当前账号没有大会员权限，无法下载。请登录大会员账号后再试。")
		}
		return nil, fmt.Errorf("B站未返回 DASH 媒体流（请确认是否需登录或大会员权限）")
	}

	video := pickVideoStream(dash.Video, targetQuality, targetCodec)
	audio := pickAudioStream(dash)

	if video == nil {
		if isBangumi || epid > 0 {
			return nil, fmt.Errorf("未找到可用的视频轨（该视频为大会员专享内容，当前账号无下载权限）")
		}
		return nil, fmt.Errorf("未找到满足条件的可用视频轨")
	}

	videoURLs := collectSortedCDNs(video.BaseURL, video.BackupURL)
	videoURL := ""
	if len(videoURLs) > 0 {
		videoURL = videoURLs[0]
	}

	var audioURLs []string
	audioURL := ""
	if audio != nil {
		audioURLs = collectSortedCDNs(audio.BaseURL, audio.BackupURL)
		if len(audioURLs) > 0 {
			audioURL = audioURLs[0]
		}
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
		VideoURLs:    videoURLs,
		AudioURL:     audioURL,
		AudioURLs:    audioURLs,
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
	if isBangumi || epid > 0 {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		params := url.Values{}
		params.Set("support_multi_audio", "true")
		params.Set("from_client", "BROWSER")
		if aid > 0 {
			params.Set("avid", strconv.FormatInt(aid, 10))
		}
		if bvid != "" {
			params.Set("bvid", bvid)
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
		reqURL := "https://api.bilibili.com/pgc/player/web/v2/playurl?" + params.Encode()
		var resp playurlAPIResp
		if err := c.GetJSON(ctx, reqURL, &resp); err != nil {
			return nil, fmt.Errorf("请求番剧媒体流失败: %w", err)
		}
		if resp.Code == 0 && resp.getDash() != nil {
			return &resp, nil
		}
		if resp.Result != nil && (resp.Result.Dash != nil || len(resp.Result.AcceptQuality) > 0 || resp.Result.VideoInfo != nil) {
			return &resp, nil
		}
		if resp.Message != "" {
			return nil, fmt.Errorf("番剧媒体流返回异常 (code=%d): %s (请确认是否需要大会员权限)", resp.Code, resp.Message)
		}
		return nil, fmt.Errorf("该视频为哔哩哔哩大会员专享内容，当前账号未开通大会员或未登录，无法下载。请登录大会员账号后再试。")
	}

	// 2. 普通视频：优先尝试官方 WBI 签名 playurl API (确保 1080P/4K/8K/杜比/Hi-Res 高清完整流)
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
	if signedQuery, err := c.SignWbiParams(ctx, paramMap); err == nil {
		wbiURL := "https://api.bilibili.com/x/player/wbi/playurl?" + signedQuery
		var wbiResp playurlAPIResp
		if err := c.GetJSON(ctx, wbiURL, &wbiResp); err == nil && wbiResp.Code == 0 && wbiResp.getDash() != nil {
			return &wbiResp, nil
		}
	}

	// 3. 次级降级：若 WBI 签名受限或接口超时，回退至未签名标准 playurl API 作为保底
	stdURL := fmt.Sprintf("https://api.bilibili.com/x/player/playurl?bvid=%s&avid=%d&cid=%d&qn=127&fnval=4048&fnver=0&fourk=1&otype=json",
		url.QueryEscape(bvid), aid, cid)
	var stdResp playurlAPIResp
	if err := c.GetJSON(ctx, stdURL, &stdResp); err == nil && stdResp.Code == 0 && stdResp.getDash() != nil {
		return &stdResp, nil
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

	targetQuality = strings.TrimSpace(targetQuality)
	targetCodec = strings.TrimSpace(targetCodec)
	targetQN := 0
	if targetQuality != "" && !strings.EqualFold(targetQuality, "highest") {
		if qn, err := strconv.Atoi(targetQuality); err == nil && qn > 0 {
			targetQN = qn
		}
	}

	// 1. 只保留真正有可下载直链的流；有些 API 响应会把直链放在 backup_url，
	// 不能只看 BaseURL，否则可能选中一个无法下载的最高画质。
	var candidates []DashStream
	for _, v := range videos {
		if hasPlayableStreamURL(v) {
			candidates = append(candidates, v)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	// 2. 如果指定了画质限制，只允许选择不高于用户要求的画质。
	if targetQN > 0 {
		var qualityCandidates []DashStream
		for _, v := range videos {
			if v.ID <= targetQN && hasPlayableStreamURL(v) {
				qualityCandidates = append(qualityCandidates, v)
			}
		}
		// 没有可接受的画质时返回 nil，让调用方明确报告“无可用流”，
		// 不能回退到更高画质造成用户实际下载结果超出设置。
		if len(qualityCandidates) == 0 {
			return nil
		}
		candidates = qualityCandidates
	}

	// 3. 显式编码是硬约束。若目标编码在允许的画质中不存在，返回 nil，
	// 避免用户选择 HEVC/AV1 后实际得到 AVC 等错误结果。
	if targetCodec != "" && !strings.EqualFold(targetCodec, "auto") {
		var codecMatches []DashStream
		for _, v := range candidates {
			if streamMatchesCodec(v, targetCodec) {
				codecMatches = append(codecMatches, v)
			}
		}
		if len(codecMatches) == 0 {
			return nil
		}
		candidates = codecMatches
	}

	// 4. 排序：ID 最高 (清晰度最高) -> 带宽/码率最高
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
	if dash == nil {
		return nil
	}

	var allAudio []DashStream
	allAudio = append(allAudio, dash.Audio...)
	allAudio = append(allAudio, dash.Dolby.Audio...)
	if dash.Flac != nil && dash.Flac.Audio != nil {
		allAudio = append(allAudio, *dash.Flac.Audio)
	}

	var best *DashStream
	for i := range allAudio {
		a := &allAudio[i]
		if !hasPlayableStreamURL(*a) {
			continue
		}
		if best == nil || a.Bandwidth > best.Bandwidth {
			best = a
		}
	}
	return best
}

func hasPlayableStreamURL(stream DashStream) bool {
	if strings.TrimSpace(stream.BaseURL) != "" {
		return true
	}
	for _, backupURL := range stream.BackupURL {
		if strings.TrimSpace(backupURL) != "" {
			return true
		}
	}
	return false
}

func streamMatchesCodec(stream DashStream, targetCodec string) bool {
	switch strings.ToUpper(strings.TrimSpace(targetCodec)) {
	case "AVC", "H.264", "H264":
		return stream.Codecid == 7
	case "HEVC", "H.265", "H265":
		return stream.Codecid == 12
	case "AV1":
		return stream.Codecid == 13
	default:
		return false
	}
}

// ReplaceCDNServer 将 URL 中的 CDN 服务器替换为指定的骨干节点
func ReplaceCDNServer(rawURL string, newHost string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Host = newHost
	return u.String()
}

// 常见顶级稳定高速骨干 CDN 列表
var BackboneCDNs = []string{
	"upos-sz-mirrorcos.bilivideo.com", // 腾讯云骨干 CDN
	"upos-sz-mirrorali.bilivideo.com", // 阿里云骨干 CDN
	"upos-sz-mirrorbos.bilivideo.com", // 百度云骨干 CDN
	"upos-sz-upcdnbwc.bilivideo.com",  // 网宿骨干 CDN
}

var reExplicitPort = regexp.MustCompile(`https?://[^/]+:\d+`)

// chooseBestCDN 挑选最优质稳定的官方骨干 CDN 节点 (彻底规避 PCDN 与限速边缘节点)
func chooseBestCDN(baseURL string, backupURLs []string) string {
	sorted := collectSortedCDNs(baseURL, backupURLs)
	if len(sorted) > 0 {
		return sorted[0]
	}
	return baseURL
}

// collectSortedCDNs 收集并排序所有可用 CDN 节点（顶级骨干网优先，规避 PCDN，保留备用节点供故障转移）
func collectSortedCDNs(baseURL string, backupURLs []string) []string {
	var rawCandidates []string
	if baseURL != "" {
		rawCandidates = append(rawCandidates, baseURL)
	}
	rawCandidates = append(rawCandidates, backupURLs...)

	// 去重
	seen := make(map[string]bool)
	var candidates []string
	for _, u := range rawCandidates {
		u = strings.TrimSpace(u)
		if u != "" && !seen[u] {
			seen[u] = true
			candidates = append(candidates, u)
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	var tier1Backbone []string // 顶级骨干网
	var tier2Regular []string  // 普通正规 CDN
	var tier3Other []string    // PCDN 或其他

	for _, u := range candidates {
		isPCDN := strings.Contains(u, "mcdn.bilivideo") || strings.Contains(u, "upos-tf-all") || reExplicitPort.MatchString(u)
		if isPCDN {
			tier3Other = append(tier3Other, u)
			continue
		}

		isBackbone := strings.Contains(u, "upos-sz-mirrorcos") ||
			strings.Contains(u, "upos-sz-mirrorali") ||
			strings.Contains(u, "upos-sz-mirrorbos") ||
			strings.Contains(u, "upos-sz-upcdnbwc")
		if isBackbone {
			tier1Backbone = append(tier1Backbone, u)
		} else {
			tier2Regular = append(tier2Regular, u)
		}
	}

	var result []string
	result = append(result, tier1Backbone...)
	result = append(result, tier2Regular...)

	// 如果没有原生骨干网，构造腾讯云骨干 CDN 备用节点作为候选追加在后，绝不覆盖置顶原生有效节点
	if len(tier1Backbone) == 0 && len(candidates) > 0 {
		boosted := ReplaceCDNServer(candidates[0], "upos-sz-mirrorcos.bilivideo.com")
		if !seen[boosted] {
			seen[boosted] = true
			result = append(result, boosted)
		}
	}

	// 最后追加兜底的其他候选 (如 PCDN 边缘节点)
	result = append(result, tier3Other...)

	if len(result) == 0 {
		result = append(result, baseURL)
	}

	return result
}
