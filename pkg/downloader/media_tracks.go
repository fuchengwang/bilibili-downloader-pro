package downloader

import (
	"context"
	"fmt"
)

type mediaTrackFailure struct {
	label  string
	stream *StreamDownloader
	err    error
}

// Start both tracks so both real entity lengths are learned from transfer
// headers immediately. A failed tiny audio probe must not hide total progress
// until an entire video has downloaded. Join every worker before returning.
func downloadMediaTracks(ctx context.Context, video, audio *StreamDownloader, threads int, progress DownloadProgressFn) *mediaTrackFailure {
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan mediaTrackFailure, 2)
	videoWorkers, audioWorkers := max(1, threads), 0
	if audio != nil {
		// Ghost's Bilibili steps apply the configured subworker count to
		// both tracks. Splitting that seed in half leaves a slow video at
		// half its requested concurrency while audio is still running.
		audioWorkers = max(1, threads)
	}
	go func() {
		results <- mediaTrackFailure{"视频轨", video, video.DownloadWithConcurrency(work, videoWorkers, progress)}
	}()
	count := 1
	if audio != nil {
		count++
		go func() {
			results <- mediaTrackFailure{"音频轨", audio, audio.DownloadWithConcurrency(work, audioWorkers, progress)}
		}()
	}
	var first *mediaTrackFailure
	for i := 0; i < count; i++ {
		r := <-results
		if r.err != nil && first == nil {
			first = &r
			cancel()
		}
	}
	if ctx.Err() != nil {
		return &mediaTrackFailure{err: ctx.Err()}
	}
	return first
}

func (f *mediaTrackFailure) Error() string { return fmt.Sprintf("%s: %v", f.label, f.err) }
