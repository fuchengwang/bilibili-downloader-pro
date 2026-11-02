package downloader

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

var errSlowStream = errors.New("CDN 持续低速，尝试换节点续传")
var errIdleStream = errors.New("CDN 数据流超过读取等待时间，尝试重连续传")

type streamReadPolicy struct {
	idle, window time.Duration
	minRate      float64
	minRemaining int64
}

var defaultStreamReadPolicy = streamReadPolicy{
	idle: 8 * time.Second, window: 20 * time.Second,
	minRate: 32 * 1024, minRemaining: 512 * 1024,
}

// Measure time inside network Read only: bandwidth reservations, progress
// callbacks and disk writes must not make a healthy CDN appear slow.
type watchedStreamBody struct {
	io.ReadCloser
	owner     *StreamDownloader
	policy    streamReadPolicy
	remaining int64
	resumable bool
	elapsed   time.Duration
	bytes     int64
}

func (s *StreamDownloader) watchResponse(body io.ReadCloser, size int64, resumable bool) io.ReadCloser {
	policy := defaultStreamReadPolicy
	if s.readPolicy != nil {
		policy = *s.readPolicy
	}
	return &watchedStreamBody{ReadCloser: body, owner: s, policy: policy, remaining: size, resumable: resumable}
}

func (s *StreamDownloader) hasBandwidthLimit() bool {
	if s.bandwidth == nil {
		return false
	}
	s.bandwidth.mu.Lock()
	defer s.bandwidth.mu.Unlock()
	return s.bandwidth.limit() > 0
}

func (b *watchedStreamBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	start := time.Now()
	closed := make(chan struct{})
	timer := time.AfterFunc(b.policy.idle, func() {
		_ = b.ReadCloser.Close() // http.Response.Body.Close unblocks Read.
		close(closed)
	})
	n, err := b.ReadCloser.Read(p)
	idle := !timer.Stop()
	if idle {
		<-closed // No timer callback may outlive this read.
	}
	b.elapsed += time.Since(start)
	b.bytes += int64(n)
	if b.remaining >= 0 {
		b.remaining -= int64(n)
	}
	if idle {
		b.monitorEvent("idle-reconnect")
		return n, errIdleStream
	}
	if err != nil {
		return n, err
	}
	if b.owner.hasBandwidthLimit() {
		b.elapsed, b.bytes = 0, 0
		return n, nil
	}
	if b.elapsed >= b.policy.window {
		slow := float64(b.bytes)/b.elapsed.Seconds() < b.policy.minRate
		b.elapsed, b.bytes = 0, 0
		// Unknown lengths and non-range servers retain the previous behavior.
		// Near completion a reconnect costs more than it saves. A shared budget
		// prevents parallel ranges or genuinely slow networks from reconnecting
		// indefinitely; after three attempts keep downloading normally.
		if slow && b.resumable && b.remaining > b.policy.minRemaining && b.owner.claimSlowRecovery() {
			b.monitorEvent("slow-reconnect")
			_ = b.ReadCloser.Close()
			b.owner.client.CloseIdleConnections()
			return n, errSlowStream
		}
	}
	return n, nil
}

func (b *watchedStreamBody) monitorEvent(event string) {
	if os.Getenv("BBDOWN_STREAM_MONITOR") == "1" {
		log.Printf("stream-monitor event=%s file=%q remaining=%d recovery_count=%d", event,
			filepath.Base(b.owner.targetPath), b.remaining, b.owner.slowRecoveries.Load())
	}
}

func (s *StreamDownloader) claimSlowRecovery() bool {
	for {
		n := s.slowRecoveries.Load()
		if n >= 3 {
			return false
		}
		if s.slowRecoveries.CompareAndSwap(n, n+1) {
			return true
		}
	}
}
