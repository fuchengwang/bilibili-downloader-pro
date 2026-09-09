package bilibili

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// mixinKeyEncTab 是 B 站 WBI 算法的固定字节重排混淆表
var mixinKeyEncTab = []int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35,
	27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13,
}

type navWbiResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		WbiImg struct {
			ImgURL string `json:"img_url"`
			SubURL string `json:"sub_url"`
		} `json:"wbi_img"`
	} `json:"data"`
}

// EnsureWbi 获取并缓存 WBI Mixin Key
func (c *Client) EnsureWbi(ctx context.Context) error {
	c.mu.RLock()
	if c.wbiMixin != "" && time.Since(c.wbiCached) < 12*time.Hour {
		c.mu.RUnlock()
		return nil
	}
	c.mu.RUnlock()

	var resp navWbiResp
	if err := c.GetJSON(ctx, "https://api.bilibili.com/x/web-interface/nav", &resp); err != nil {
		return fmt.Errorf("failed to fetch nav for WBI keys: %w", err)
	}

	img := extractSubFilename(resp.Data.WbiImg.ImgURL)
	sub := extractSubFilename(resp.Data.WbiImg.SubURL)
	if img == "" || sub == "" {
		return fmt.Errorf("empty WBI keys returned from nav API")
	}

	raw := img + sub
	var sb strings.Builder
	for _, idx := range mixinKeyEncTab {
		if idx < len(raw) {
			sb.WriteByte(raw[idx])
		}
	}
	key := sb.String()
	if len(key) > 32 {
		key = key[:32]
	}

	c.mu.Lock()
	c.wbiMixin = key
	c.wbiCached = time.Now()
	c.mu.Unlock()

	return nil
}

func extractSubFilename(urlStr string) string {
	if i := strings.LastIndexByte(urlStr, '/'); i >= 0 {
		urlStr = urlStr[i+1:]
	}
	if i := strings.LastIndexByte(urlStr, '.'); i >= 0 {
		urlStr = urlStr[:i]
	}
	return urlStr
}

// SignWbiParams 对参数字典按字典序排序并计算 w_rid 签名
func (c *Client) SignWbiParams(ctx context.Context, params map[string]string) (string, error) {
	if err := c.EnsureWbi(ctx); err != nil {
		return "", err
	}
	c.mu.RLock()
	mixinKey := c.wbiMixin
	c.mu.RUnlock()

	// 浅拷贝避免多任务并发签名时修改调用方 map 导致 concurrent map writes 致命崩溃
	clonedParams := make(map[string]string, len(params)+1)
	for k, v := range params {
		clonedParams[k] = v
	}
	if _, ok := clonedParams["wts"]; !ok {
		clonedParams["wts"] = strconv.FormatInt(time.Now().Unix(), 10)
	}

	// 1. 字典序排列 key
	var keys []string
	for k := range clonedParams {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// 2. 拼接 query
	var queryParts []string
	for _, k := range keys {
		v := clonedParams[k]
		// 过滤字符: ! ' ( ) *
		cleaned := strings.Map(func(r rune) rune {
			if r == '!' || r == '\'' || r == '(' || r == ')' || r == '*' {
				return -1
			}
			return r
		}, v)
		queryParts = append(queryParts, url.QueryEscape(k)+"="+url.QueryEscape(cleaned))
	}

	queryString := strings.Join(queryParts, "&")

	// 3. 计算 MD5
	sum := md5.Sum([]byte(queryString + mixinKey))
	wRid := hex.EncodeToString(sum[:])

	return queryString + "&w_rid=" + wRid, nil
}
