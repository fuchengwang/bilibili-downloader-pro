package downloader

import (
	"context"
	"fmt"
	"sync"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

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
			if err == nil && (value == nil || value.QualityID != original.QualityID || value.Codec != original.Codec) {
				err = fmt.Errorf("刷新后的媒体画质或编码发生变化，请重新添加任务")
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
