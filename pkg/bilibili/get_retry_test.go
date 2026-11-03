package bilibili

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type retryTransport func(*http.Request) (*http.Response, error)

func (f retryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReadOnlyGETRetriesTransportAndTruncatedBodies(t *testing.T) {
	var attempts atomic.Int32
	c := &Client{httpClient: &http.Client{Transport: retryTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Referer") != Referer || r.Header.Get("User-Agent") == "" {
			t.Error("retry lost request headers")
		}
		switch attempts.Add(1) {
		case 1:
			return nil, io.EOF
		case 2:
			return &http.Response{StatusCode: 200, Body: io.NopCloser(&truncatedGETReader{}), Header: make(http.Header)}, nil
		default:
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":0}`)), Header: make(http.Header)}, nil
		}
	})}}
	body, err := c.GetBytes(context.Background(), "https://api.bilibili.com/test", nil)
	if err != nil || string(body) != `{"code":0}` || attempts.Load() != 3 {
		t.Fatalf("GET recovery: attempts=%d err=%v body=%s", attempts.Load(), err, body)
	}
}

type truncatedGETReader struct{}

func (*truncatedGETReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestReadOnlyGETDoesNotRetryPermissionOrBusinessErrors(t *testing.T) {
	for _, status := range []int{403, 412, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"code":-10403}`)
			}))
			defer server.Close()
			c := &Client{httpClient: server.Client()}
			_, _ = c.GetBytes(context.Background(), server.URL, nil)
			if calls.Load() != 1 {
				t.Fatal("permission/business response was replayed")
			}
		})
	}
}
