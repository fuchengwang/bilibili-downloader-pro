package main

import (
	"context"
	"fmt"
	"sync"

	"bilibili_downloader/pkg/bilibili"
)

// Validate only the requested videos. Unrelated unavailable entries, or a
// truncated season list, must not prevent downloading a verified selection.
func collectionSelection(ctx context.Context, entries []bilibili.EpisodeInfo, videos []string, cids []int64, fetch func(context.Context, bilibili.EpisodeInfo) (*bilibili.CollectionVideo, error)) ([]bilibili.EpisodeInfo, error) {
	wanted := make(map[string]bool, len(videos))
	for _, id := range videos {
		wanted[id] = true
	}
	selected := make(map[int64]bool, len(cids))
	for _, cid := range cids {
		selected[cid] = true
	}
	var jobs []bilibili.EpisodeInfo
	for _, entry := range entries {
		if (len(videos) > 0 && wanted[entry.BVID]) || (len(videos) == 0 && selected[entry.CID]) {
			jobs = append(jobs, entry)
			delete(wanted, entry.BVID)
		}
	}
	if len(wanted) > 0 {
		return nil, fmt.Errorf("所选视频不在当前返回的合集列表中，请重新解析")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	groups := make([]*bilibili.CollectionVideo, len(jobs))
	indices := make(chan int)
	var wg sync.WaitGroup
	var failureOnce sync.Once
	var firstFailure error
	for n := 0; n < min(4, len(jobs)); n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range indices {
				group, err := fetch(ctx, jobs[i])
				if err != nil {
					failureOnce.Do(func() {
						firstFailure = fmt.Errorf("读取 %s 的分P失败：%w", jobs[i].Title, err)
						cancel()
					})
					return
				}
				groups[i] = group
			}
		}()
	}
dispatch:
	for i := range jobs {
		select {
		case indices <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(indices)
	wg.Wait()
	if firstFailure != nil {
		return nil, firstFailure
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var result []bilibili.EpisodeInfo
	found := make(map[int64]bool)
	for i, group := range groups {
		if group == nil || group.BVID != jobs[i].BVID || len(group.Parts) == 0 {
			return nil, fmt.Errorf("视频 %s 的分P信息不完整", jobs[i].BVID)
		}
		for _, part := range group.Parts {
			if selected[part.CID] && !found[part.CID] {
				result = append(result, part)
				found[part.CID] = true
			}
		}
	}
	if len(found) != len(selected) {
		return nil, fmt.Errorf("所选分P已变化，请重新解析后选择")
	}
	return result, nil
}
