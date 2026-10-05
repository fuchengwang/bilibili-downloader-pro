package update

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	ErrRateLimited        = errors.New("update request rate limited")
	ErrDownloadInProgress = errors.New("an update download is already running")
	ErrMonitorRunning     = errors.New("automatic update checks are already running")
	ErrReleaseChanged     = errors.New("update is no longer the recommended release; check again")
)

// Config needs only ServerURL, AppID, and CurrentVersion for a native client.
type Config struct {
	ServerURL       string
	AppID           string
	CurrentVersion  string
	OS              string        // Empty: runtime.GOOS.
	Arch            string        // Empty: runtime.GOARCH.
	Timeout         time.Duration // Check timeout; default 10 seconds.
	DownloadTimeout time.Duration // Entire download, including mirrors; default 30 minutes.
	HTTPClient      *http.Client  // Optional transport/proxy customization; never modified by the SDK.
}

// APIError is machine-readable. Applications own the wording shown to users.
type APIError struct {
	StatusCode int
	Code       int
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("update API returned HTTP %d", e.StatusCode)
}

func (e *APIError) Unwrap() error {
	if e.StatusCode == http.StatusTooManyRequests {
		return ErrRateLimited
	}
	return nil
}

type checkFlight struct {
	done   chan struct{}
	result *CheckResult
	err    error
}

type Client struct {
	cfg                     Config
	checkHTTP, downloadHTTP *http.Client
	mu                      sync.Mutex
	flight                  *checkFlight
	retryUntil              time.Time
	downloading             bool
	monitorRunning          bool
}

// New performs no network requests and starts no goroutines.
func New(cfg Config) (*Client, error) {
	cfg.ServerURL = strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
	u, err := webURL(cfg.ServerURL)
	if err != nil || u.RawQuery != "" {
		return nil, errors.New("server_url must be an HTTP(S) base URL without query or credentials")
	}
	cfg.AppID = strings.TrimSpace(cfg.AppID)
	if !validAppID(cfg.AppID) {
		return nil, errors.New("app_id must contain 1-64 letters, digits, hyphens or underscores")
	}
	cfg.CurrentVersion, _, err = ParseVersion(cfg.CurrentVersion)
	if err != nil {
		return nil, err
	}
	if cfg.OS == "" {
		cfg.OS = runtime.GOOS
	}
	if cfg.Arch == "" {
		cfg.Arch = runtime.GOARCH
	}
	if !ValidPlatform(cfg.OS, cfg.Arch, false) {
		return nil, errors.New("unsupported update platform")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.DownloadTimeout == 0 {
		cfg.DownloadTimeout = 30 * time.Minute
	}
	if cfg.Timeout < 0 || cfg.DownloadTimeout < 0 {
		return nil, errors.New("timeouts must be positive")
	}
	base := http.Client{}
	if cfg.HTTPClient != nil {
		base = *cfg.HTTPClient
	}
	redirect := base.CheckRedirect
	base.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if _, err := webURL(req.URL.String()); err != nil {
			return err
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("HTTPS redirect downgrade rejected")
		}
		if redirect != nil {
			return redirect(req, via)
		}
		return nil
	}
	checkHTTP, downloadHTTP := base, base
	checkHTTP.Timeout = cfg.Timeout
	downloadHTTP.Timeout = cfg.DownloadTimeout
	return &Client{cfg: cfg, checkHTTP: &checkHTTP, downloadHTTP: &downloadHTTP}, nil
}

func validAppID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func webURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("invalid HTTP(S) URL")
	}
	return u, nil
}

// Check coalesces concurrent calls. A waiting caller may cancel its own wait.
// A failed check is an error, never a successful "no update" response.
func (c *Client) Check(ctx context.Context) (*CheckResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if time.Now().Before(c.retryUntil) {
		err := &APIError{StatusCode: 429, Code: 429, Message: "update check is cooling down", RetryAfter: time.Until(c.retryUntil)}
		c.mu.Unlock()
		return nil, err
	}
	if flight := c.flight; flight != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-flight.done:
			return cloneResult(flight.result), flight.err
		}
	}
	flight := &checkFlight{done: make(chan struct{})}
	c.flight = flight
	c.mu.Unlock()
	result, err := c.check(ctx)
	c.mu.Lock()
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 429 {
		c.retryUntil = time.Now().Add(apiErr.RetryAfter)
	}
	flight.result, flight.err = result, err
	c.flight = nil
	close(flight.done)
	c.mu.Unlock()
	return cloneResult(result), err
}

func (c *Client) check(ctx context.Context) (*CheckResult, error) {
	u, _ := url.Parse(c.cfg.ServerURL + "/api/v1/updates/check")
	u.RawQuery = url.Values{"app_id": {c.cfg.AppID}, "current_version": {c.cfg.CurrentVersion}, "os": {c.cfg.OS}, "arch": {c.cfg.Arch}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.checkHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("check update: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("read update response: %w", err)
	}
	if len(body) > 1<<20 {
		return nil, errors.New("update response is too large")
	}
	var envelope Response[json.RawMessage]
	decodeErr := json.Unmarshal(body, &envelope)
	if resp.StatusCode != 200 || decodeErr == nil && (!envelope.Success || envelope.Code != 200) {
		e := &APIError{StatusCode: resp.StatusCode, Code: envelope.Code, Message: envelope.Message}
		if resp.StatusCode == 429 {
			var data struct {
				RetryAfter int64 `json:"retry_after"`
			}
			_ = json.Unmarshal(envelope.Data, &data)
			e.RetryAfter = retryAfter(resp.Header.Get("Retry-After"), time.Now())
			if e.RetryAfter == 0 && data.RetryAfter > 0 && data.RetryAfter <= 86400 {
				e.RetryAfter = time.Duration(data.RetryAfter) * time.Second
			}
			if e.RetryAfter == 0 {
				e.RetryAfter = 30 * time.Second
			}
		}
		return nil, e
	}
	if decodeErr != nil {
		return nil, errors.New("invalid update response JSON")
	}
	var result CheckResult
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		return nil, errors.New("invalid update result")
	}
	var required struct {
		HasUpdate *bool `json:"has_update"`
	}
	if err := json.Unmarshal(envelope.Data, &required); err != nil || required.HasUpdate == nil {
		return nil, errors.New("update result is missing has_update")
	}
	if result.AppID != c.cfg.AppID || result.CurrentVersion != c.cfg.CurrentVersion {
		return nil, errors.New("update response does not match this client")
	}
	if result.HasUpdate {
		if result.Release == nil {
			return nil, errors.New("update response has no release")
		}
		if err := c.validateRelease(*result.Release); err != nil {
			return nil, err
		}
	} else if result.Release != nil {
		return nil, errors.New("inconsistent update response")
	}
	return &result, nil
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 && seconds <= 86400 {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}

func cloneResult(r *CheckResult) *CheckResult {
	if r == nil {
		return nil
	}
	copyResult := *r
	if r.Release != nil {
		copyRelease := *r.Release
		copyRelease.Artifact.Sources = append([]Source(nil), r.Release.Artifact.Sources...)
		copyResult.Release = &copyRelease
	}
	return &copyResult
}

func (c *Client) validateRelease(release Release) error {
	version, numbers, err := ParseVersion(release.Version)
	if err != nil || version != release.Version {
		return errors.New("invalid release version")
	}
	_, current, _ := ParseVersion(c.cfg.CurrentVersion)
	newer := false
	for i := range numbers {
		if numbers[i] != current[i] {
			newer = numbers[i] > current[i]
			break
		}
	}
	if !newer {
		return errors.New("update would not upgrade the current version")
	}
	a := release.Artifact
	if a.OS != c.cfg.OS || !(a.Arch == c.cfg.Arch || a.OS == "darwin" && a.Arch == "universal") {
		return errors.New("update package does not match this platform")
	}
	if a.FileName == "" || a.FileName == "." || a.FileName == ".." || len(a.FileName) > 255 || strings.ContainsAny(a.FileName, "/\\:\x00\r\n") {
		return errors.New("invalid package filename")
	}
	if a.Size <= 0 || a.Size > 2<<30 {
		return errors.New("invalid package size")
	}
	hash, err := hex.DecodeString(a.SHA256)
	if err != nil || len(hash) != 32 {
		return errors.New("invalid package SHA-256")
	}
	if len(a.Sources) == 0 || len(a.Sources) > 10 {
		return errors.New("invalid package sources")
	}
	for _, source := range a.Sources {
		if _, err := webURL(source.URL); err != nil {
			return err
		}
	}
	return nil
}

// Confirm checks that a previously offered package is still the current
// recommendation. Call before handing a downloaded file to an installer.
func (c *Client) Confirm(ctx context.Context, release Release) error {
	result, err := c.Check(ctx)
	if err != nil {
		return err
	}
	if !result.HasUpdate || result.Release.Version != release.Version || result.Release.Artifact.Size != release.Artifact.Size || !strings.EqualFold(result.Release.Artifact.SHA256, release.Artifact.SHA256) {
		return ErrReleaseChanged
	}
	return nil
}
