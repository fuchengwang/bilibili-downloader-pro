package bilibili

import (
	"encoding/json"
	"log"

	"bilibili_downloader/pkg/utils"
)

func cloneUserInfo(info *UserInfo) *UserInfo {
	if info == nil {
		return nil
	}
	copy := *info
	return &copy
}

// GetCachedUserInfo performs no network requests. Nil means credentials exist
// but their account profile has not yet been fetched; it does not mean logout.
func (c *Client) GetCachedUserInfo() *UserInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cookieData == nil || c.cookieData.SessData == "" {
		return &UserInfo{IsLogin: false}
	}
	return cloneUserInfo(c.cookieData.UserInfo)
}

// Keep the profile in the existing private cookie file so logout and session
// replacement also remove it. A late response must not restore an old account.
func (c *Client) cacheUserInfo(session string, info *UserInfo) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cookieData == nil || c.cookieData.SessData != session {
		return false
	}
	c.cookieData.UserInfo = cloneUserInfo(info)
	data, err := json.MarshalIndent(c.cookieData, "", "  ")
	if err == nil {
		err = utils.AtomicWriteFile(c.cookiesPath(), data, 0600)
	}
	if err != nil {
		// A cache write failure must not turn a successful login into a failure.
		log.Printf("保存账号资料缓存失败: %v", err)
	}
	return true
}
