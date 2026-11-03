package downloader

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func routeFixture(primary, direct http.RoundTripper) *mediaRouteTransport {
	return &mediaRouteTransport{primary: primary, direct: direct, proxy: func(*http.Request) (*url.URL, error) { return url.Parse("http://proxy.invalid:17893") }, directUntil: make(map[string]time.Time)}
}

func routeResponse(req *http.Request) *http.Response {
	return &http.Response{StatusCode: 206, Body: io.NopCloser(strings.NewReader("media")), Header: make(http.Header), Request: req}
}

func TestMediaRouteRecoversProxyEOFAndReusesWorkingRoute(t *testing.T) {
	var primaryCalls, directCalls atomic.Int32
	tr := routeFixture(streamRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		primaryCalls.Add(1)
		return nil, io.EOF
	}), streamRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		directCalls.Add(1)
		if req.Header.Get("Range") != "bytes=4096-8191" || req.Header.Get("If-Range") != "version" || !req.Close {
			t.Error("route fallback changed the range request")
		}
		return routeResponse(req), nil
	}))
	req, _ := http.NewRequest("GET", "https://cdn.bilivideo.com/media", nil)
	req.Header.Set("Range", "bytes=4096-8191")
	req.Header.Set("If-Range", "version")
	req.Close = true
	for i := 0; i < 2; i++ {
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := tr.RoundTrip(req.Clone(req.Context()))
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
		}()
	}
	wg.Wait()
	if primaryCalls.Load() != 1 || directCalls.Load() != 18 {
		t.Fatalf("repeated broken proxy instead of healthy route: primary=%d direct=%d", primaryCalls.Load(), directCalls.Load())
	}
}

func TestMediaRouteDoesNotBypassHealthyProxyOrNonNetworkFailures(t *testing.T) {
	for _, tc := range []struct {
		name, host, method string
		err                error
		status             int
		cancelled, noProxy bool
	}{
		{name: "healthy proxy", status: 206},
		{name: "HTTP permission error", status: 403},
		{name: "certificate verification", err: x509.UnknownAuthorityError{}},
		{name: "invalid request", err: errors.New("invalid request")},
		{name: "cancelled", err: io.EOF, cancelled: true},
		{name: "other website", host: "example.com", err: io.EOF},
		{name: "not a CDN suffix", host: "bilivideo.com.example.com", err: io.EOF},
		{name: "non GET", method: "POST", err: io.EOF},
		{name: "already direct", err: io.EOF, noProxy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := routeFixture(streamRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				resp := routeResponse(req)
				resp.StatusCode = tc.status
				return resp, nil
			}), streamRoundTripperFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected direct fallback"); return nil, io.EOF }))
			if tc.noProxy {
				tr.proxy = nil
			}
			host, method := tc.host, tc.method
			if host == "" {
				host = "cdn.bilivideo.com"
			}
			if method == "" {
				method = "GET"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			req, _ := http.NewRequestWithContext(ctx, method, "https://"+host+"/media", nil)
			resp, err := tr.RoundTrip(req)
			if err != tc.err {
				t.Fatalf("original error lost: %v", err)
			}
			if resp != nil {
				resp.Body.Close()
			}
		})
	}
}

func TestMediaRouteReturnsToProxyWhenDirectConnectionFails(t *testing.T) {
	var calls atomic.Int32
	tr := routeFixture(streamRoundTripperFunc(func(req *http.Request) (*http.Response, error) { calls.Add(1); return routeResponse(req), nil }), streamRoundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF }))
	req, _ := http.NewRequest("GET", "https://cdn.bilivideo.com/media", nil)
	proxy, _ := tr.proxy(req)
	tr.directUntil[req.URL.Scheme+"://"+req.URL.Host+"|"+proxy.String()] = time.Now().Add(time.Minute)
	for i := 0; i < 2; i++ {
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if calls.Load() != 2 || len(tr.directUntil) != 0 {
		t.Fatal("failed direct route stayed preferred")
	}
}
