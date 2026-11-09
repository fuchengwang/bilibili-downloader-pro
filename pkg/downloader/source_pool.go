package downloader

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Each manager shares the connection budget across tracks and tasks. Waiting
// for a slot does not consume a CDN read/header timeout.
type connectionBudget chan struct{}

func (b connectionBudget) acquire(ctx context.Context) (func(), error) {
	if b == nil {
		return func() {}, nil
	}
	select {
	case b <- struct{}{}:
		return func() { <-b }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type sourceState struct {
	url      string
	failures int
	ready    time.Time
	missing  bool
}

// Shared by all workers of one track. URL refresh is single flight and failed
// sources cool down, so parallel 403/429 responses cannot create retry storms.
type sourcePool struct {
	mu         sync.Mutex
	sources    []sourceState
	refreshing chan struct{}
	refreshed  time.Time
	refresh    URLRefresher
}

func newSourcePool(urls []string, refresh URLRefresher) *sourcePool {
	p := &sourcePool{refresh: refresh}
	for _, u := range filterStreamURLs(urls) {
		p.sources = append(p.sources, sourceState{url: u})
	}
	return p
}

func (p *sourcePool) next(ctx context.Context) (string, error) {
	for {
		p.mu.Lock()
		if len(p.sources) == 0 {
			p.mu.Unlock()
			return "", fmt.Errorf("无有效下载 URL")
		}
		best := -1
		for i := range p.sources {
			if p.sources[i].missing {
				continue
			}
			// Preserve the API's preference once a source's cooldown expires.
			// Comparing timestamps alone makes an untouched backup (zero time)
			// win forever after a single transient error on the primary.
			if !p.sources[i].ready.After(time.Now()) {
				best = i
				break
			}
			if best < 0 || p.sources[i].ready.Before(p.sources[best].ready) {
				best = i
			}
		}
		if best < 0 {
			p.mu.Unlock()
			return "", fmt.Errorf("媒体请求 HTTP 404：所有候选地址均未找到媒体")
		}
		s := p.sources[best]
		p.mu.Unlock()
		if delay := time.Until(s.ready); delay > 0 {
			if err := waitForRetry(ctx, delay); err != nil {
				return "", err
			}
			continue
		}
		return s.url, nil
	}
}

func (p *sourcePool) notFound(url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.sources {
		if p.sources[i].url == url {
			p.sources[i].missing = true
		}
	}
}

func (p *sourcePool) failed(url string, delay time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.sources {
		s := &p.sources[i]
		if s.url == url {
			s.failures++
			if delay <= 0 {
				delay = time.Duration(1<<min(s.failures-1, 4)) * 100 * time.Millisecond
			}
			s.ready = time.Now().Add(delay)
		}
	}
}

func (p *sourcePool) succeeded(url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.sources {
		if p.sources[i].url == url {
			p.sources[i].failures = 0
			p.sources[i].ready = time.Time{}
		}
	}
}

func (p *sourcePool) refreshURLs(ctx context.Context, failed string) error {
	p.mu.Lock()
	if p.refresh == nil {
		p.mu.Unlock()
		return nil
	}
	if done := p.refreshing; done != nil {
		p.mu.Unlock()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if time.Since(p.refreshed) < time.Second {
		p.mu.Unlock()
		return nil
	}
	done := make(chan struct{})
	p.refreshing = done
	p.mu.Unlock()
	urls, err := p.refresh(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refreshed = time.Now()
	if clean := filterStreamURLs(urls); err == nil && len(clean) > 0 {
		next := make([]sourceState, 0, len(clean))
		for _, u := range clean {
			s := sourceState{url: u}
			for _, old := range p.sources {
				if old.url == u {
					s = old
					break
				}
			}
			if u == failed && s.ready.Before(time.Now().Add(time.Second)) {
				s.ready = time.Now().Add(time.Second)
			}
			next = append(next, s)
		}
		p.sources = next
	}
	p.refreshing = nil
	close(done)
	return err
}

func retryAfter(h http.Header) time.Duration {
	value := strings.TrimSpace(h.Get("Retry-After"))
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil {
		return time.Duration(max(0, min(seconds, 120))) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(0, min(time.Until(date), 120*time.Second))
	}
	return time.Second
}
