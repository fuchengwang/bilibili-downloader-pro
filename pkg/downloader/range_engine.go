package downloader

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	errRangeUnsupported = errors.New("server does not support ranged downloads")
	errEntityChanged    = errors.New("媒体内容已变化，重新下载")
)

const minimumSplitSize int64 = 64 * 1024

// Keep network requests small even when an ownership interval is large. A
// long-lived Bilibili response can exhaust its initial burst and then crawl
// at playback rate. Renew bounded ranges without losing the written prefix.
const maximumRequestSize int64 = 2 * 1024 * 1024

// Go implementation of the dynamic-range/direct-write design described in
// docs/ghost-download-core.md. All range ownership and writes share mu, so a
// worker may donate its unwritten tail without overlapping another writer.
type rangeTransfer struct {
	mu                              sync.Mutex
	s                               *StreamDownloader
	file                            *os.File
	pool                            *sourcePool
	ranges                          []*byteRange
	total                           int64
	etag, modified                  string
	validatorKey                    string
	threads                         int
	confirmed, canRange, sequential bool
	changed                         chan struct{}
	progress                        DownloadProgressFn
}

func (s *StreamDownloader) downloadCore(ctx context.Context, threads int, progress DownloadProgressFn) (result error) {
	defer func() { s.recordDiagnostic(result) }()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(s.targetPath) == "" {
		return fmt.Errorf("目标文件路径为空")
	}
	if len(filterStreamURLs(s.urls)) == 0 {
		return fmt.Errorf("无有效下载 URL")
	}
	if err := os.MkdirAll(filepath.Dir(s.targetPath), 0755); err != nil {
		return err
	}
	threads = max(1, min(threads, 8))
	initial := resumableStreamSize(s.targetPath)
	record, recordErr := readRangeRecord(s.targetPath)
	if recordErr == nil && initial > 0 && !s.representationChanged(record) &&
		(initial == record.Total || s.representation != "" && record.Representation == "") {
		release, err := s.connections.acquire(ctx)
		if err != nil {
			return err
		}
		_, err = s.probeTotalSize(ctx)
		release()
		if err != nil {
			// A temporary connection failure is not evidence of different media.
			return fmt.Errorf("暂时无法校验下载断点，已保留缓存: %w", err)
		}
	} else if s.totalSize <= 0 && s.sizeHint <= 0 && !s.probed && !s.skipSizeProbe {
		_, _ = s.GetTotalSize(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	pool := newSourcePool(s.urls, s.refresher)
	sequential := false
	for restart := 0; restart < 3; restart++ {
		e, err := s.prepareTransfer(pool, threads, sequential, progress)
		if err != nil {
			return err
		}
		// The manager already counted target/manifest bytes before starting us.
		// Only the difference (legacy migration or a reset) belongs here.
		if delta := e.received() - initial; delta != 0 {
			s.reportRestored(delta, progress)
		}
		initial = e.received()
		err = e.run(ctx)
		closeErr := e.file.Close()
		if err == nil {
			err = closeErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errRangeUnsupported) || errors.Is(err, errEntityChanged) {
			initial = e.received()
			if initial != 0 {
				s.reportRestored(-initial, progress)
			}
			initial = 0
			// Persist the zero progress marker BEFORE truncating a sparse file.
			// A crash between these operations can only repeat bytes, never skip.
			if s.totalSize > 0 {
				if saveErr := writeRangeRecord(s.targetPath, rangeRecord{Version: 1, Total: s.totalSize, Representation: s.representation, Ranges: []byteRange{{End: s.totalSize}}}); saveErr != nil {
					return saveErr
				}
			}
			if truncateErr := os.Truncate(s.targetPath, 0); truncateErr != nil {
				return truncateErr
			}
			if errors.Is(err, errRangeUnsupported) {
				sequential = true
				threads = 1
			}
			continue
		}
		if err == nil {
			for i := 0; i < 8; i++ {
				_ = os.Remove(streamSegmentPath(s.targetPath, i))
			}
		} else if initial == 0 && e.received() == 0 {
			_ = os.Remove(rangeRecordPath(s.targetPath))
			_ = os.Remove(s.targetPath)
			_ = os.Remove(rangeMarkerPath(s.targetPath))
		}
		return err
	}
	return fmt.Errorf("媒体范围或内容反复变化，已保留缓存，请稍后重试")
}

func (s *StreamDownloader) reportRestored(delta int64, progress DownloadProgressFn) {
	if s.restoredProgressFn != nil {
		s.restoredProgressFn(delta)
	} else if progress != nil {
		progress(delta)
	}
}

func (s *StreamDownloader) prepareTransfer(pool *sourcePool, threads int, sequential bool, progress DownloadProgressFn) (*rangeTransfer, error) {
	e := &rangeTransfer{s: s, pool: pool, total: s.totalSize, threads: threads, sequential: sequential, progress: progress, changed: make(chan struct{}, 1)}
	r, recordErr := readRangeRecord(s.targetPath)
	_, statErr := os.Stat(rangeRecordPath(s.targetPath))
	hasRecord := !os.IsNotExist(statErr)
	if _, err := os.Stat(rangeMarkerPath(s.targetPath)); !os.IsNotExist(err) {
		hasRecord = true
	}
	reset := hasRecord && recordErr != nil
	if recordErr == nil {
		sameSource := r.ValidatorKey == "" || r.ValidatorKey == s.probeValidatorKey
		reset = s.representationChanged(r) || (s.totalSize > 0 && r.Total != s.totalSize) ||
			(sameSource && validatorChanged(r.ETag, r.LastModified, s.probeETag, s.probeModified))
		if r.received() == r.Total || (r.received() > 0 && s.representation != "" && r.Representation == "") {
			reset = reset || !s.confirmsCachedEntity(r)
		}
	} else if s.representation != "" {
		// Legacy raw prefixes have neither a track identity nor an HTTP validator.
		reset = true
	}
	if recordErr == nil && !reset {
		e.total = r.Total
		e.etag = r.ETag
		e.modified = r.LastModified
		e.validatorKey = r.ValidatorKey
		for _, b := range r.Ranges {
			e.ranges = append(e.ranges, &byteRange{Start: b.Start, Position: b.Position, End: b.End})
		}
	}
	file, err := os.OpenFile(s.targetPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	e.file = file
	if len(e.ranges) == 0 {
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return nil, err
		}
		prefix := info.Size()
		// A legacy contiguous prefix has no durable proof of completion.
		if reset || (e.total > 0 && prefix >= e.total) {
			prefix = 0
		}
		end := e.total
		if end <= 0 {
			end = math.MaxInt64
		}
		e.ranges = []*byteRange{{Position: prefix, End: end}}
		e.etag = s.probeETag
		e.modified = s.probeModified
		e.validatorKey = s.probeValidatorKey
		if e.total > 0 {
			// From here on the manifest, rather than file length, owns progress.
			if err := e.checkpoint(); err != nil {
				file.Close()
				return nil, err
			}
		}
		if reset || info.Size() > prefix {
			if err := file.Truncate(prefix); err != nil {
				file.Close()
				return nil, err
			}
		}
	}
	if err := ensureRangeMarker(s.targetPath); err != nil {
		file.Close()
		return nil, err
	}
	if e.total > 0 {
		s.setTotalSize(e.total)
	}
	return e, nil
}

func (s *StreamDownloader) representationChanged(r rangeRecord) bool {
	return r.Representation != "" && s.representation != "" && r.Representation != s.representation
}

func (s *StreamDownloader) confirmsCachedEntity(r rangeRecord) bool {
	// Length alone (or a weak validator) cannot prove equal bytes. ETags
	// are only comparable for the resource which issued them.
	tag := r.ETag
	return r.Total == s.totalSize && r.ValidatorKey != "" && r.ValidatorKey == s.probeValidatorKey &&
		len(tag) >= 2 && tag[0] == '"' && tag[len(tag)-1] == '"' && tag == s.probeETag
}

func validatorChanged(oldTag, oldDate, newTag, newDate string) bool {
	if oldTag != "" && newTag != "" {
		return oldTag != newTag
	}
	return oldDate != "" && newDate != "" && oldDate != newDate
}

func (e *rangeTransfer) received() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	var n int64
	for _, b := range e.ranges {
		n += b.Position - b.Start
	}
	return n
}

func (e *rangeTransfer) notify() {
	select {
	case e.changed <- struct{}{}:
	default:
	}
}

func (e *rangeTransfer) checkpoint() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.total <= 0 {
		return nil
	}
	// Never advertise writes that have not reached the filesystem. Holding mu
	// also excludes splits while serializing the partition.
	if err := e.file.Sync(); err != nil {
		return fmt.Errorf("保存媒体数据失败: %w", err)
	}
	r := rangeRecord{Version: 1, Total: e.total, ETag: e.etag, LastModified: e.modified, ValidatorKey: e.validatorKey, Representation: e.s.representation}
	for _, b := range e.ranges {
		r.Ranges = append(r.Ranges, byteRange{Start: b.Start, Position: b.Position, End: b.End})
	}
	sort.Slice(r.Ranges, func(i, j int) bool { return r.Ranges[i].Start < r.Ranges[j].Start })
	return writeRangeRecord(e.s.targetPath, r)
}

func (e *rangeTransfer) claim(ctx context.Context) (*byteRange, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e.mu.Lock()
		var largest *byteRange
		pending := false
		for _, b := range e.ranges {
			if b.Position >= b.End {
				continue
			}
			pending = true
			if !b.active && (e.confirmed || !e.anyActiveLocked()) {
				b.active = true
				e.mu.Unlock()
				return b, nil
			}
			if b.active && (largest == nil || b.End-b.Position > largest.End-largest.Position) {
				largest = b
			}
		}
		if e.confirmed && e.canRange && largest != nil && largest.End-largest.Position >= 2*minimumSplitSize && len(e.ranges) < 4096 {
			cut := largest.Position + (largest.End-largest.Position)/2
			b := &byteRange{Start: cut, Position: cut, End: largest.End, active: true}
			largest.End = cut
			e.ranges = append(e.ranges, b)
			e.mu.Unlock()
			return b, nil
		}
		e.mu.Unlock()
		if !pending {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-e.changed:
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (e *rangeTransfer) anyActiveLocked() bool {
	for _, b := range e.ranges {
		if b.active {
			return true
		}
	}
	return false
}

func (e *rangeTransfer) run(ctx context.Context) error {
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	policy := defaultStreamAccelerationPolicy
	if e.s.acceleration != nil {
		policy = *e.s.acceleration
	}
	policy.maxWorkers = max(policy.maxWorkers, e.threads)
	results := make(chan error, policy.maxWorkers)
	remainingWorkers := 0
	spawn := func() {
		remainingWorkers++
		go func() {
			for {
				b, err := e.claim(work)
				if err != nil || b == nil {
					results <- err
					return
				}
				rangeCtx, cancelRange := context.WithCancel(work)
				e.mu.Lock()
				b.cancel = cancelRange
				retired := b.retired
				e.mu.Unlock()
				if !retired {
					err = e.transferRange(rangeCtx, b)
				}
				cancelRange()
				e.mu.Lock()
				b.active = false
				b.cancel = nil
				retired = b.retired
				e.mu.Unlock()
				e.notify()
				if retired {
					// Reassign this slot to the largest remaining tail. Permanently
					// retiring every absorbed worker collapses long downloads to
					// one connection after their initial burst.
					continue
				}
				if err != nil {
					results <- err
					return
				}
			}
		}()
	}
	for i := 0; i < e.threads; i++ {
		spawn()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	accelerateTick := time.NewTicker(policy.sample)
	defer accelerateTick.Stop()
	accelerator := streamAccelerator{policy: policy}
	lastBytes, lastSample := e.received(), time.Now()
	var first error
	for remainingWorkers > 0 {
		select {
		case err := <-results:
			remainingWorkers--
			if err != nil && first == nil {
				first = err
				cancel()
			}
		case <-ticker.C:
			if first == nil {
				if err := e.checkpoint(); err != nil {
					first = err
					cancel()
				}
			}
		case now := <-accelerateTick.C:
			received := e.received()
			rate := float64(received-lastBytes) / now.Sub(lastSample).Seconds()
			lastBytes, lastSample = received, now
			e.mu.Lock()
			canSplit, total := e.canRange, e.total
			e.mu.Unlock()
			if first == nil && canSplit && !e.s.hasBandwidthLimit() {
				for count := accelerator.additional(now, rate, remainingWorkers, total-received); count > 0; count-- {
					spawn()
				}
			}
		}
	}
	if err := e.checkpoint(); err != nil && first == nil {
		first = err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if first != nil {
		return first
	}
	if e.total <= 0 {
		return fmt.Errorf("下载响应未提供可验证的总长度，拒绝将 EOF 视为完成")
	}
	if e.received() != e.total {
		return fmt.Errorf("下载范围未完整，已保留缓存")
	}
	return nil
}

func (e *rangeTransfer) transferRange(ctx context.Context, b *byteRange) error {
	var last error
	failures := 0
	ignoredRanges := 0
	for failures < 8 {
		if err := ctx.Err(); err != nil {
			return err
		}
		e.mu.Lock()
		start := b.Position
		end := e.total
		if end <= 0 {
			end = e.s.sizeHint
		}
		done := start >= b.End
		if e.sequential && start > 0 && !done {
			b.Position = 0
			e.mu.Unlock()
			e.s.reportRestored(-start, e.progress)
			if err := e.checkpoint(); err != nil {
				return err
			}
			if err := e.file.Truncate(0); err != nil {
				return err
			}
			e.mu.Lock()
			start = 0
		}
		ranged := !e.sequential && (start > 0 || e.threads > 1 || e.confirmed || e.s.sizeHint > 0)
		validator := e.etag
		if validator == "" || strings.HasPrefix(validator, "W/") {
			validator = e.modified
		}
		e.mu.Unlock()
		if done {
			return nil
		}
		url, err := e.pool.next(ctx)
		if err != nil {
			return err
		}
		release, err := e.s.connections.acquire(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			release()
			return err
		}
		setMediaHeaders(req)
		if ranged {
			// A new bounded transfer gets a fresh CDN connection. Reusing one
			// indefinitely can preserve a per-connection playback-rate limit.
			req.Close = true
			span := fmt.Sprintf("bytes=%d-", start)
			if end > 0 {
				span += fmt.Sprint(min(end, start+maximumRequestSize) - 1)
			}
			req.Header.Set("Range", span)
			e.mu.Lock()
			matchingSource := e.validatorKey == "" || e.validatorKey == mediaValidatorKey(url)
			e.mu.Unlock()
			if validator != "" && matchingSource {
				req.Header.Set("If-Range", validator)
			}
		}
		resp, err := e.s.client.Do(req)
		if err != nil {
			release()
			last = err
			e.s.recordDiagnostic(err)
			e.pool.failed(url, 0)
			failures++
			continue
		}
		status := resp.StatusCode
		if status != 200 && status != 206 {
			_ = resp.Body.Close()
			release()
			last = fmt.Errorf("媒体请求 HTTP %d", status)
			e.s.recordDiagnostic(last)
			if status == 416 {
				return errEntityChanged
			}
			if status == http.StatusNotFound {
				e.pool.notFound(url)
				failures++
				continue
			}
			if status == 429 || status == 503 {
				e.pool.failed(url, retryAfter(resp.Header))
			} else {
				e.pool.failed(url, 0)
			}
			if status == 401 || status == 403 || status == 410 {
				if err := e.pool.refreshURLs(ctx, url); err != nil {
					e.s.recordDiagnostic(err)
				}
			}
			if status == 400 || status == 405 || status == 451 {
				return last
			}
			failures++
			continue
		}
		err = e.acceptResponse(resp, b, start, ranged)
		if err == nil {
			err = e.copyResponse(ctx, resp, b)
		}
		_ = resp.Body.Close()
		release()
		if err == nil {
			e.pool.succeeded(url)
			failures = 0
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errEntityChanged) {
			return err
		}
		if errors.Is(err, errRangeUnsupported) {
			// A single ignored Range must not discard every healthy worker's
			// bytes and permanently collapse a verified parallel transfer.
			ignoredRanges++
			e.mu.Lock()
			confirmed := e.confirmed
			e.mu.Unlock()
			if !confirmed && ignoredRanges >= 3 {
				return errRangeUnsupported
			}
		}
		var local *os.PathError
		if errors.As(err, &local) {
			return err
		} // disk errors are never network retries
		if e.total <= 0 {
			return err
		}
		last = err
		e.s.recordDiagnostic(err)
		e.pool.failed(url, 0)
		e.mu.Lock()
		advanced := b.Position > start
		e.mu.Unlock()
		if advanced {
			failures = 0
		} else {
			failures++
		}
	}
	return fmt.Errorf("下载连续重试耗尽: %w", last)
}

func mediaValidatorKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	// Authentication query strings rotate without changing the selected
	// media. Validators belong to one origin/path, never to other CDNs.
	return fmt.Sprintf("%x", sha256.Sum256([]byte(u.Scheme+"://"+u.Host+u.EscapedPath())))
}

func (e *rangeTransfer) acceptResponse(resp *http.Response, b *byteRange, start int64, ranged bool) error {
	e.mu.Lock()
	key := ""
	if resp.Request != nil {
		key = mediaValidatorKey(resp.Request.URL.String())
	}
	sameSource := e.validatorKey == "" || e.validatorKey == key
	changed := sameSource && validatorChanged(e.etag, e.modified, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"))
	e.mu.Unlock()
	if isLikelyNonMediaResponse(resp) {
		return fmt.Errorf("媒体响应类型异常: %s", resp.Header.Get("Content-Type"))
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return fmt.Errorf("媒体响应使用了不支持的内容编码")
	}
	total := resp.ContentLength
	canRange := strings.EqualFold(resp.Header.Get("Accept-Ranges"), "bytes")
	if resp.StatusCode == 206 {
		first, last, size, err := parseContentRange(resp.Header.Get("Content-Range"))
		if err != nil || first != start || size <= 0 || last < first || (resp.ContentLength >= 0 && resp.ContentLength != last-first+1) {
			return errRangeUnsupported
		}
		total = size
		canRange = true
	} else if ranged && !changed {
		return errRangeUnsupported
	}
	e.mu.Lock()
	if (e.total > 0 && total > 0 && total != e.total) || changed {
		if total > 0 {
			e.s.setTotalSize(total)
		}
		e.s.rememberProbeValidators(resp)
		e.mu.Unlock()
		return errEntityChanged
	}
	first := !e.confirmed
	if e.total <= 0 && total > 0 {
		e.total = total
		b.End = total
		e.s.setTotalSize(total)
	}
	if e.etag == "" && sameSource {
		e.etag = resp.Header.Get("ETag")
	}
	if e.modified == "" && sameSource {
		e.modified = resp.Header.Get("Last-Modified")
	}
	if e.validatorKey == "" {
		e.validatorKey = key
	}
	if first {
		e.confirmed = true
		e.canRange = canRange && !e.sequential
	}
	if first && e.canRange && e.total > 0 && len(e.ranges) == 1 {
		count := min(e.threads, int((b.End-b.Position)/minimumSplitSize))
		if count > 1 {
			length := (b.End - b.Position) / int64(count)
			oldEnd := b.End
			b.End = b.Position + length
			for i := 1; i < count; i++ {
				start := b.End + int64(i-1)*length
				end := start + length
				if i == count-1 {
					end = oldEnd
				}
				e.ranges = append(e.ranges, &byteRange{Start: start, Position: start, End: end})
			}
		}
	}
	e.mu.Unlock()
	if first {
		if err := e.checkpoint(); err != nil {
			return err
		}
		// This allocation happens only after a durable range record exists.
		if e.total > 0 {
			if err := e.file.Truncate(e.total); err != nil {
				return err
			}
		}
	}
	e.notify()
	return nil
}

func (e *rangeTransfer) copyResponse(ctx context.Context, resp *http.Response, b *byteRange) error {
	e.mu.Lock()
	canRange := e.canRange
	e.mu.Unlock()
	body := e.s.watchResponse(resp.Body, resp.ContentLength, canRange)
	reader := bandwidthReader{ctx: ctx, reader: body, limiter: e.s.bandwidth}
	responseEnd := e.total
	if responseEnd <= 0 {
		responseEnd = math.MaxInt64
	}
	if resp.StatusCode == http.StatusPartialContent {
		_, end, _, err := parseContentRange(resp.Header.Get("Content-Range"))
		if err == nil {
			responseEnd = end + 1
		}
		// Some CDNs honor the start but ignore a requested range end. Bound
		// actual consumption as well, so a full-tail response cannot silently
		// turn a short transfer into a long throttled connection.
		e.mu.Lock()
		responseEnd = min(responseEnd, b.Position+maximumRequestSize)
		e.mu.Unlock()
	}
	buf := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		e.mu.Lock()
		if b.retired {
			e.mu.Unlock()
			return nil
		}
		if b.Position >= responseEnd {
			e.mu.Unlock()
			return nil
		}
		if b.Position >= b.End {
			e.absorbEmptyRightLocked(b, responseEnd)
		}
		remaining := min(b.End, responseEnd) - b.Position
		e.mu.Unlock()
		if remaining <= 0 {
			return nil
		}
		n, readErr := reader.Read(buf[:min(int64(len(buf)), remaining)])
		if n > 0 {
			e.mu.Lock()
			if b.retired {
				e.mu.Unlock()
				return nil
			}
			count := min(int64(n), b.End-b.Position)
			written, err := e.file.WriteAt(buf[:count], b.Position)
			b.Position += int64(written)
			if written > 0 && e.progress != nil {
				e.progress(int64(written))
			}
			done := b.Position >= b.End
			// A split can shrink our range while Read is in flight. If that read
			// crossed the donated boundary, close this response: its cursor is
			// ahead of our write position and cannot safely absorb a neighbour.
			if done && count == int64(n) && e.absorbEmptyRightLocked(b, responseEnd) {
				done = false
			}
			e.mu.Unlock()
			if err != nil {
				return err
			}
			if int64(written) != count {
				return io.ErrShortWrite
			}
			if done {
				return nil
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				if e.total <= 0 {
					return fmt.Errorf("下载响应未提供可验证的总长度，拒绝将 EOF 视为完成")
				}
				e.mu.Lock()
				position := b.Position
				e.mu.Unlock()
				// A verified bounded response may end before this worker's
				// ownership interval. Continue with its next range normally.
				if position == responseEnd {
					return nil
				}
				return io.ErrUnexpectedEOF
			}
			return readErr
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
}

// A request extends to the file's end. If the next worker has received no
// bytes, the existing connection can take over its range without reconnecting.
// Revoke ownership and cancel that worker before extending this writer.
func (e *rangeTransfer) absorbEmptyRightLocked(left *byteRange, responseEnd int64) bool {
	if !e.canRange {
		return false
	}
	absorbed := false
	for {
		found := false
		for i, right := range e.ranges {
			if right != left && right.Start == left.End && right.Position == right.Start && right.End <= responseEnd {
				right.retired = true
				if right.cancel != nil {
					right.cancel()
				}
				left.End = right.End
				e.ranges = append(e.ranges[:i], e.ranges[i+1:]...)
				found, absorbed = true, true
				break
			}
		}
		if !found {
			return absorbed
		}
	}
}
