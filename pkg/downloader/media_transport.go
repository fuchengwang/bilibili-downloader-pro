package downloader

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"bilibili_downloader/pkg/bilibili"
)

// Reuse DNS/TLS/HTTP2 connections for the whole queue, rather than creating a
// transport per track. A separate budget includes response bodies, not just
// TCP connections (HTTP2 can multiplex many bodies on one connection).
var mediaTransport = &http.Transport{
	Proxy:             http.ProxyFromEnvironment,
	DialContext:       (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	ForceAttemptHTTP2: true,
	MaxIdleConns:      64, MaxIdleConnsPerHost: 16, MaxConnsPerHost: 16,
	IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second,
	ResponseHeaderTimeout: 12 * time.Second, ExpectContinueTimeout: time.Second,
	DisableCompression: true,
}

var globalConnections = make(connectionBudget, 24)

// The desktop process can inherit a terminal proxy even when that proxy cannot
// carry Bilibili CDN traffic. Switching CDN names alone then repeats the same
// broken route. Keep the configured path first, but recover pre-response
// connection failures using a verified direct route for media GETs only.
type mediaRouteTransport struct {
	primary, direct http.RoundTripper
	proxy           func(*http.Request) (*url.URL, error)
	mu              sync.Mutex
	directUntil     map[string]time.Time
}

func newMediaRouteTransport(primary *http.Transport) *mediaRouteTransport {
	direct := primary.Clone()
	direct.Proxy = nil
	return &mediaRouteTransport{primary: primary, direct: direct, proxy: primary.Proxy, directUntil: make(map[string]time.Time)}
}

// Both routes reuse transports across tracks and tasks. Tests which install a
// dedicated transport still keep their isolated dialer and TLS configuration.
var sharedMediaRoutes = newMediaRouteTransport(mediaTransport)

func mediaTransportForStream() http.RoundTripper {
	if sharedMediaRoutes.primary == mediaTransport {
		return sharedMediaRoutes
	}
	return newMediaRouteTransport(mediaTransport)
}

func isBilibiliMediaHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return strings.HasSuffix(host, ".bilivideo.com") || strings.HasSuffix(host, ".bilivideo.cn") || host == "upos-hz-mirrorakam.akamaized.net"
}

func recoverableRouteError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var network net.Error
	return errors.As(err, &network) && network.Timeout()
}

func (t *mediaRouteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.proxy == nil || req.Method != http.MethodGet || req.Body != nil || !isBilibiliMediaHost(req.URL.Hostname()) {
		return t.primary.RoundTrip(req)
	}
	proxy, err := t.proxy(req)
	if err != nil || proxy == nil {
		return t.primary.RoundTrip(req)
	}
	key := req.URL.Scheme + "://" + req.URL.Host + "|" + proxy.String()
	t.mu.Lock()
	preferDirect := time.Now().Before(t.directUntil[key])
	t.mu.Unlock()
	first, second := t.primary, t.direct
	if preferDirect {
		first, second = second, first
	}
	resp, err := first.RoundTrip(req)
	if err == nil {
		return resp, nil
	}
	if req.Context().Err() != nil || !recoverableRouteError(err) {
		return resp, err
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	// No body has been consumed and the GET has no upload body. Preserve Range,
	// If-Range, cancellation and TLS verification on the alternative route.
	resp, fallbackErr := second.RoundTrip(req.Clone(req.Context()))
	if fallbackErr == nil && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent) {
		t.mu.Lock()
		if preferDirect {
			delete(t.directUntil, key)
		} else {
			for k, until := range t.directUntil {
				if time.Now().After(until) {
					delete(t.directUntil, k)
				}
			}
			t.directUntil[key] = time.Now().Add(time.Minute)
		}
		t.mu.Unlock()
	}
	return resp, fallbackErr
}

func setMediaHeaders(req *http.Request) {
	req.Header.Set("User-Agent", bilibili.BrowserUA)
	req.Header.Set("Referer", bilibili.Referer)
	req.Header.Set("Accept-Encoding", "identity")
}
