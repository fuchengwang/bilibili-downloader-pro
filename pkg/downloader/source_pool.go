package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errAllSourcesRejected = errors.New("all candidate sources rejected")

var singleSourceBackoff = []time.Duration{
	200 * time.Millisecond,
	400 * time.Millisecond,
	800 * time.Millisecond,
	1200 * time.Millisecond,
	1600 * time.Millisecond,
	2000 * time.Millisecond,
	2500 * time.Millisecond,
}

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
	url          string
	failures     int
	ready        time.Time
	demotedUntil time.Time
	missing      bool
	rejected     bool
}

func (s sourceState) preferredAfter() time.Time {
	return laterTime(s.ready, s.demotedUntil)
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
		if err := ctx.Err(); err != nil {
			return "", err
		}
		p.mu.Lock()
		if len(p.sources) == 0 {
			p.mu.Unlock()
			return "", fmt.Errorf("无有效下载 URL")
		}
		best := -1
		available := 0
		hasRejected := false
		now := time.Now()
		for i := range p.sources {
			if p.sources[i].rejected {
				hasRejected = true
			}
			if p.sources[i].missing || p.sources[i].rejected {
				continue
			}
			available++
			// Preserve the API's preference once a source's cooldown expires.
			// Comparing timestamps alone makes an untouched backup (zero time)
			// win forever after a single transient error on the primary.
			ready := p.sources[i].preferredAfter()
			if !ready.After(now) {
				best = i
				break
			}
			if best < 0 || ready.Before(p.sources[best].preferredAfter()) {
				best = i
			}
		}
		if best < 0 {
			refresh := p.refresh != nil && (p.refreshing != nil || time.Since(p.refreshed) >= time.Second)
			p.mu.Unlock()
			if refresh {
				if err := p.refreshURLs(ctx, ""); err != nil {
					return "", fmt.Errorf("刷新下载地址失败: %w", err)
				}
				continue
			}
			if hasRejected {
				return "", errAllSourcesRejected
			}
			return "", &mediaHTTPError{code: http.StatusNotFound, detail: "：所有候选地址均未找到媒体"}
		}
		s := p.sources[best]
		p.mu.Unlock()
		ready := s.preferredAfter()
		// Demotion is a preference. A slow node can still make progress if
		// all other candidates are gone. Respect ordinary retry delays.
		if available == 1 {
			ready = s.ready
		}
		if delay := time.Until(ready); delay > 0 {
			if err := waitForRetry(ctx, delay); err != nil {
				return "", err
			}
			continue
		}
		return s.url, nil
	}
}

func (p *sourcePool) hasHealthyAlternativeLocked(excludeURL string, now time.Time) bool {
	for i := range p.sources {
		s := &p.sources[i]
		if s.url != excludeURL && !s.missing && !s.rejected && !s.preferredAfter().After(now) {
			return true
		}
	}
	return false
}

func (p *sourcePool) reject(url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.sources {
		if p.sources[i].url == url {
			p.sources[i].rejected = true
		}
	}
}

func (p *sourcePool) candidateURLs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	urls := make([]string, 0, len(p.sources))
	for i := range p.sources {
		s := &p.sources[i]
		if !s.missing && !s.rejected {
			urls = append(urls, s.url)
		}
	}
	return urls
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
	p.failedLocked(url, delay, time.Now())
}

func (p *sourcePool) demote(url string, preferDelay time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if preferDelay > 0 && p.hasHealthyAlternativeLocked(url, now) {
		for i := range p.sources {
			s := &p.sources[i]
			if s.url == url {
				s.failures++
				s.demotedUntil = laterTime(s.demotedUntil, now.Add(preferDelay))
			}
		}
		return
	}
	p.failedLocked(url, 0, now)
}

func (p *sourcePool) failedLocked(url string, delay time.Duration, now time.Time) {
	hasAlt := delay <= 0 && p.hasHealthyAlternativeLocked(url, now)
	for i := range p.sources {
		s := &p.sources[i]
		if s.url == url {
			s.failures++
			d := delay
			if d <= 0 {
				if hasAlt {
					d = time.Duration(1<<min(s.failures-1, 4)) * 500 * time.Millisecond
				} else {
					d = singleSourceBackoff[min(s.failures-1, len(singleSourceBackoff)-1)]
				}
			}
			s.ready = laterTime(s.ready, now.Add(d))
		}
	}
}

func (p *sourcePool) succeeded(url string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for i := range p.sources {
		if p.sources[i].url == url && !p.sources[i].ready.After(now) {
			p.sources[i].failures = 0
			p.sources[i].ready = time.Time{}
		}
	}
}

func laterTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
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
	if ctx.Err() == nil {
		p.refreshed = time.Now()
	}
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
