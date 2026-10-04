package bilibili

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// TargetType 解析得到的目标资源类型
type TargetType string

const (
	TargetNormal  TargetType = "normal"  // 普通视频 (单P / 多P / UGC合集)
	TargetBangumi TargetType = "bangumi" // 番剧 / 国创 / 电影 / 电视剧 / 纪录片
	TargetCheese  TargetType = "cheese"  // 课程
)

// ParsedTarget 统一的解析目标结构
type ParsedTarget struct {
	Type     TargetType `json:"type"`
	RawInput string     `json:"rawInput"`
	BVID     string     `json:"bvid"`
	AID      string     `json:"aid"`
	EPID     string     `json:"epid"`
	SSID     string     `json:"ssid"`
	Page     int        `json:"page"`
}

var (
	reURL = regexp.MustCompile(`https?://[a-zA-Z0-9.\-_/?:&=%#+]+`)
	reBV  = regexp.MustCompile(`(?i)(BV1[a-zA-Z0-9]{9})`)
	reAV  = regexp.MustCompile(`(?i)av(\d+)`)
	reEP  = regexp.MustCompile(`(?i)ep(\d+)`)
	reSS  = regexp.MustCompile(`(?i)ss(\d+)`)
)

// ParseInput 解析用户输入的任何字符串（支持 URL、短链、口令分享文本、纯 ID）
func (c *Client) ParseInput(ctx context.Context, input string) (*ParsedTarget, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("请输入有效的 B 站视频链接或 ID")
	}

	// 1. 如果包含 URL，先提取 URL
	if match := reURL.FindString(input); match != "" {
		input = match
	}

	page := 1
	// 2. 如果是 b23.tv 短链，先展开为真实目标 URL
	if strings.Contains(input, "b23.tv") {
		finalURL, err := c.FinalURL(ctx, input)
		if err == nil && finalURL != "" {
			input = finalURL
		}
	}
	lower := strings.ToLower(input)
	episodeType := TargetBangumi
	if strings.Contains(lower, "/cheese/") || strings.HasPrefix(lower, "cheese/") {
		episodeType = TargetCheese
	}

	// 尝试从 URL 查询参数中解析分 P 序号
	if u, err := url.Parse(input); err == nil {
		if pVal := u.Query().Get("p"); pVal != "" {
			if p, err := strconv.Atoi(pVal); err == nil && p > 0 {
				page = p
			}
		}
		if epVal := u.Query().Get("ep_id"); epVal != "" {
			return &ParsedTarget{
				Type:     episodeType,
				RawInput: input,
				EPID:     epVal,
				Page:     page,
			}, nil
		}
		if ssVal := u.Query().Get("season_id"); ssVal != "" {
			return &ParsedTarget{
				Type:     episodeType,
				RawInput: input,
				SSID:     ssVal,
				Page:     page,
			}, nil
		}
	}

	// Course and bangumi IDs occupy different namespaces despite sharing ep/ss.
	if episodeType == TargetCheese || strings.Contains(lower, "bangumi/play") || strings.Contains(lower, "/ep") || strings.Contains(lower, "/ss") {
		if m := reEP.FindStringSubmatch(input); len(m) > 1 {
			return &ParsedTarget{
				Type:     episodeType,
				RawInput: input,
				EPID:     m[1],
				Page:     page,
			}, nil
		}
		if m := reSS.FindStringSubmatch(input); len(m) > 1 {
			return &ParsedTarget{
				Type:     episodeType,
				RawInput: input,
				SSID:     m[1],
				Page:     page,
			}, nil
		}
		if episodeType == TargetCheese {
			return nil, fmt.Errorf("课堂链接缺少课时 ep 号或课程 ss 号")
		}
	}

	// 4. 普通视频链接或 BV 号
	if m := reBV.FindStringSubmatch(input); len(m) > 1 {
		bvid := "BV1" + m[1][3:] // 规范化
		return &ParsedTarget{
			Type:     TargetNormal,
			RawInput: input,
			BVID:     bvid,
			Page:     page,
		}, nil
	}

	// 5. av 号
	if m := reAV.FindStringSubmatch(input); len(m) > 1 {
		return &ParsedTarget{
			Type:     TargetNormal,
			RawInput: input,
			AID:      m[1],
			Page:     page,
		}, nil
	}

	// 6. 番剧纯 ep 号或 ss 号兜底
	if m := reEP.FindStringSubmatch(input); len(m) > 1 {
		return &ParsedTarget{
			Type:     TargetBangumi,
			RawInput: input,
			EPID:     m[1],
			Page:     page,
		}, nil
	}
	if m := reSS.FindStringSubmatch(input); len(m) > 1 {
		return &ParsedTarget{
			Type:     TargetBangumi,
			RawInput: input,
			SSID:     m[1],
			Page:     page,
		}, nil
	}

	return nil, fmt.Errorf("未能识别所输入的链接或视频ID: %s", input)
}
