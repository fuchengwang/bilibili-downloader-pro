package downloader

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"sync"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

func mediaRepresentationKey(urls []string) string {
	for _, raw := range urls {
		if path := mediaURLPath(raw); path != "" {
			return fmt.Sprintf("%x", sha256.Sum256([]byte(path)))
		}
	}
	return ""
}

func mediaURLPath(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		// The signed query and CDN host may change for the same track.
		return u.EscapedPath()
	}
	return ""
}

func sameRefreshedTrack(before, after []string) bool {
	for _, raw := range before {
		path := mediaURLPath(raw)
		for _, candidate := range after {
			if path != "" && path == mediaURLPath(candidate) {
				return true
			}
		}
	}
	return len(before) == 0 && len(after) == 0
}

// A single playurl refresh serves every range/track. Keep the original quality
// and codec: concatenating bytes from a newly selected representation is unsafe.
func playbackRefresher(original *bilibili.StreamSelection, fetch func(context.Context) (*bilibili.StreamSelection, error)) func(context.Context) (*bilibili.StreamSelection, error) {
	var mu sync.Mutex
	var running chan struct{}
	var last time.Time
	var selection *bilibili.StreamSelection
	var lastErr error
	return func(ctx context.Context) (*bilibili.StreamSelection, error) {
		for {
			mu.Lock()
			if running != nil {
				done := running
				mu.Unlock()
				select {
				case <-done:
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if time.Since(last) < time.Second {
				value, err := selection, lastErr
				mu.Unlock()
				return value, err
			}
			done := make(chan struct{})
			running = done
			mu.Unlock()
			value, err := fetch(ctx)
			if err == nil && (value == nil || value.QualityID != original.QualityID || value.Codec != original.Codec ||
				!sameRefreshedTrack(original.VideoURLs, value.VideoURLs) || !sameRefreshedTrack(original.AudioURLs, value.AudioURLs)) {
				err = fmt.Errorf("刷新后的媒体轨道发生变化，请重试任务")
			}
			mu.Lock()
			last = time.Now()
			selection, lastErr = value, err
			running = nil
			close(done)
			mu.Unlock()
			return value, err
		}
	}
}
