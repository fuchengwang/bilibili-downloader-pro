package bilibili

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// PlaybackError preserves a platform-confirmed reason without exposing media URLs or account credentials.
type PlaybackError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
	Detail  string `json:"detail,omitempty"`
}

func (e *PlaybackError) Error() string { return e.Message + "。" + e.Hint }

type PlaybackInfo struct {
	Qualities []QualityOption `json:"qualities"`
	Error     *PlaybackError  `json:"error,omitempty"`
}

func playbackError(kind, detail string) *PlaybackError {
	e := &PlaybackError{Kind: kind, Detail: detail}
	switch kind {
	case "charge_required":
		e.Message = "该视频需充电观看，当前账号暂无完整观看权限"
		e.Hint = "请在B站视频页开通对应充电权益；已充电请登录对应账号后重试。"
	case "login_required":
		e.Message = "需要登录B站账号，或当前登录已失效"
		e.Hint = "请登录或重新登录后重试。"
	case "vip_required":
		e.Message = "当前账号没有该视频所需的大会员权限"
		e.Hint = "请在B站确认观看要求，或登录有权限的账号后重试。"
	case "purchase_required":
		e.Message = "当前账号尚未获得该视频的购买观看权限"
		e.Hint = "请在B站确认购买状态，或登录已购买的账号后重试。"
	case "preview_only":
		e.Message = "当前账号只能观看试看内容，无法下载完整视频"
		e.Hint = "请在B站确认完整观看权限后重试。"
	case "region_restricted":
		e.Message = "该视频在当前地区暂不可用"
		e.Hint = "请在B站视频页查看地区限制。"
	case "network":
		e.Message = "连接B站失败，暂时无法获取下载地址"
		e.Hint = "请检查网络后重试。"
	default:
		e.Kind = "unavailable"
		e.Message = "暂时无法获取这个视频的下载地址"
		e.Hint = "请稍后重试，或在B站检查是否能完整播放。"
	}
	return e
}

func PlaybackErrorInfo(err error) *PlaybackError {
	var known *PlaybackError
	if errors.As(err, &known) {
		return known
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return playbackError("network", "")
	}
	return playbackError("unavailable", "")
}

func playbackAPIError(code int, message string) *PlaybackError {
	detail := fmt.Sprintf("B站播放接口返回 code=%d: %s", code, message)
	switch {
	case code == -101 || strings.Contains(message, "未登录") || strings.Contains(message, "登录失效"):
		return playbackError("login_required", detail)
	case strings.Contains(message, "充电") || strings.Contains(strings.ToLower(message), "upower"):
		return playbackError("charge_required", detail)
	case strings.Contains(message, "大会员"):
		return playbackError("vip_required", detail)
	case strings.Contains(message, "购买") || strings.Contains(message, "付费"):
		return playbackError("purchase_required", detail)
	case strings.Contains(message, "地区") || strings.Contains(message, "区域"):
		return playbackError("region_restricted", detail)
	default:
		return playbackError("unavailable", detail)
	}
}

// checkNormalPlaybackAccess uses the same account as playurl. Missing or failed
// metadata never implies a payment restriction; a positive grant stays playable.
func (c *Client) checkNormalPlaybackAccess(ctx context.Context, bvid string, aid, cid int64) *PlaybackError {
	params := url.Values{"cid": {strconv.FormatInt(cid, 10)}}
	if bvid != "" {
		params.Set("bvid", bvid)
	} else {
		params.Set("aid", strconv.FormatInt(aid, 10))
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Exclusive  *previewFlag `json:"is_upower_exclusive"`
			CanPlay    *previewFlag `json:"is_upower_play"`
			PayPreview previewFlag  `json:"is_ugc_pay_preview"`
		} `json:"data"`
	}
	if err := c.GetJSON(ctx, "https://api.bilibili.com/x/player/v2?"+params.Encode(), &resp); err != nil || resp.Code != 0 {
		return nil
	}
	if resp.Data.Exclusive != nil && bool(*resp.Data.Exclusive) && resp.Data.CanPlay != nil && !bool(*resp.Data.CanPlay) {
		return playbackError("charge_required", "B站播放器确认：充电专属，当前账号无完整播放权限")
	}
	if bool(resp.Data.PayPreview) {
		return playbackError("purchase_required", "B站播放器仅授权付费试看")
	}
	return nil
}

func (c *Client) GetPlaybackInfo(ctx context.Context, bvid string, aid, cid, epid int64, isBangumi, isCheese bool) *PlaybackInfo {
	qualities, err := c.GetAvailableQualities(ctx, bvid, aid, cid, epid, isBangumi, isCheese)
	info := &PlaybackInfo{Qualities: qualities}
	if err != nil {
		info.Error = PlaybackErrorInfo(err)
	}
	return info
}

// EpisodeURL keeps a video's part number separate from its collection index.
func EpisodeURL(ep *EpisodeInfo, isBangumi, isCheese bool) string {
	if isCheese && ep.EPID > 0 {
		return fmt.Sprintf("https://www.bilibili.com/cheese/play/ep%d", ep.EPID)
	}
	if isBangumi && ep.EPID > 0 {
		return fmt.Sprintf("https://www.bilibili.com/bangumi/play/ep%d", ep.EPID)
	}
	var video string
	if ep.BVID != "" {
		video = ep.BVID
	} else if ep.AID > 0 {
		video = fmt.Sprintf("av%d", ep.AID)
	} else {
		return ""
	}
	link := "https://www.bilibili.com/video/" + url.PathEscape(video) + "/"
	if ep.Page > 1 {
		link += "?p=" + strconv.Itoa(ep.Page)
	}
	return link
}
