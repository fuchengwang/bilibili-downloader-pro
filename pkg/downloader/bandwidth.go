package downloader

import (
	"context"
	"io"
	"sync"
	"time"
)

// One bucket is shared by every task, track and range. Acquire before reading
// the network, without reservations that could survive cancellation. A small
// bucket bounds bursts and rechecks live settings at least every 100 ms.
type bandwidthLimiter struct {
	mu      sync.Mutex
	limit   func() int64 // bytes per second; zero means unlimited
	rate    int64
	tokens  float64
	updated time.Time
}

func (l *bandwidthLimiter) acquire(ctx context.Context, requested int) (int, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if l == nil {
			return requested, nil
		}
		l.mu.Lock()
		rate := l.limit()
		now := time.Now()
		if rate != l.rate || l.updated.IsZero() {
			l.rate, l.tokens, l.updated = rate, 0, now
		}
		if rate <= 0 {
			l.mu.Unlock()
			return requested, nil
		}
		capacity := float64(rate) / 10
		if capacity < 1 {
			capacity = 1
		}
		l.tokens += now.Sub(l.updated).Seconds() * float64(rate)
		if l.tokens > capacity {
			l.tokens = capacity
		}
		l.updated = now
		n := requested
		if n > 32*1024 {
			n = 32 * 1024
		}
		if float64(n) > capacity {
			n = int(capacity)
		}
		if l.tokens >= float64(n) {
			l.tokens -= float64(n)
			l.mu.Unlock()
			return n, nil
		}
		wait := time.Duration((float64(n) - l.tokens) / float64(rate) * float64(time.Second))
		if wait > 100*time.Millisecond {
			wait = 100 * time.Millisecond
		}
		l.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
}

// Readers can return fewer bytes than their allowance (including EOF). Return
// the unused portion so short network reads do not artificially lower speed.
func (l *bandwidthLimiter) refund(unused int) {
	if l == nil || unused <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.rate <= 0 {
		return
	}
	l.tokens += float64(unused)
	capacity := float64(l.rate) / 10
	if capacity < 1 {
		capacity = 1
	}
	if l.tokens > capacity {
		l.tokens = capacity
	}
}

type bandwidthReader struct {
	ctx     context.Context
	reader  io.Reader
	limiter *bandwidthLimiter
}

func (r bandwidthReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, err := r.limiter.acquire(r.ctx, len(p))
	if err != nil {
		return 0, err
	}
	read, readErr := r.reader.Read(p[:n])
	r.limiter.refund(n - read)
	return read, readErr
}
