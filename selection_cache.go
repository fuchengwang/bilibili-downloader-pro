package main

import (
	"strconv"
	"sync"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

const selectionCacheTTL = 20 * time.Minute

type selectionCacheEntry[V any] struct {
	value   V
	expires time.Time
}

// Store only server-verified metadata. Fresh parses still fetch the current
// collection, and expired or unseen parts use the normal verification path.
type selectionCache[K comparable, V any] struct {
	mu      sync.Mutex
	entries map[K]selectionCacheEntry[V]
}

func (c *selectionCache[K, V]) get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, ok := c.entries[key]; ok && time.Now().Before(entry.expires) {
		return entry.value, true
	}
	delete(c.entries, key)
	var zero V
	return zero, false
}

func (c *selectionCache[K, V]) put(key K, value V, limit int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[K]selectionCacheEntry[V])
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= limit {
		var oldestKey K
		var oldest time.Time
		for k, entry := range c.entries {
			if oldest.IsZero() || entry.expires.Before(oldest) {
				oldestKey, oldest = k, entry.expires
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = selectionCacheEntry[V]{value, time.Now().Add(selectionCacheTTL)}
}

func selectionDetailKey(target bilibili.ParsedTarget) string {
	prefix := string(target.Type) + ":"
	switch {
	case target.EPID != "":
		return prefix + "ep:" + target.EPID
	case target.SSID != "":
		return prefix + "ss:" + target.SSID
	case target.BVID != "":
		return prefix + "bv:" + target.BVID
	case target.AID != "" && target.AID != "0":
		return prefix + "av:" + target.AID
	default:
		return ""
	}
}

func (a *App) rememberSelectionDetail(target *bilibili.ParsedTarget, detail *bilibili.VideoDetail) {
	if key := selectionDetailKey(*target); key != "" {
		a.selectionDetails.put(key, detail, 80)
	}
	if detail.Type == bilibili.TargetNormal {
		for _, alias := range []bilibili.ParsedTarget{
			{Type: detail.Type, BVID: detail.BVID},
			{Type: detail.Type, AID: strconv.FormatInt(detail.AID, 10)},
		} {
			if key := selectionDetailKey(alias); key != "" {
				a.selectionDetails.put(key, detail, 80)
			}
		}
		return
	}
	// Batch requests use the first episode ID as the season's lookup key.
	for _, episode := range detail.Episodes {
		if episode.EPID > 0 {
			key := selectionDetailKey(bilibili.ParsedTarget{Type: detail.Type, EPID: strconv.FormatInt(episode.EPID, 10)})
			a.selectionDetails.put(key, detail, 80)
			break
		}
	}
}
