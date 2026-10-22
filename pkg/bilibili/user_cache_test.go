package bilibili

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const profileResponse = `{"code":0,"data":{"isLogin":true,"mid":42,"uname":"Account A","face":"https://example.test/avatar.png","vipType":1,"vipStatus":1}}`

func profileClient(t *testing.T, transport playurlTransport) *Client {
	t.Helper()
	c := &Client{cookiePath: filepath.Join(t.TempDir(), "cookies.json"), httpClient: &http.Client{Transport: transport}}
	if err := c.SetCookies(&CookieData{SessData: "session-a", DedeUID: "42", Cookies: map[string]string{"custom": "keep"}}); err != nil {
		t.Fatal(err)
	}
	return c
}

func profileHTTPResponse(r *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestUserProfileCacheRestoresLocallyAfterRestart(t *testing.T) {
	requests := 0
	c := profileClient(t, func(r *http.Request) (*http.Response, error) {
		requests++
		return profileHTTPResponse(r, profileResponse), nil
	})
	if c.GetCachedUserInfo() != nil {
		t.Fatal("stored credentials without a profile must be unknown, not logged out")
	}
	if _, err := c.GetUserInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted := &Client{cookiePath: c.cookiePath}
	restarted.loadCookies()
	info := restarted.GetCachedUserInfo()
	if info == nil || !info.IsLogin || info.Uname != "Account A" || requests != 1 {
		t.Fatal("local startup did not restore the saved account without a network request")
	}
	info.Uname = "external mutation"
	snapshot := restarted.GetCookies()
	snapshot.UserInfo.Uname = "another mutation"
	if restarted.GetCachedUserInfo().Uname != "Account A" {
		t.Fatal("cache snapshots exposed mutable state")
	}
	if snapshot.SessData != "session-a" || snapshot.Cookies["custom"] != "keep" {
		t.Fatal("profile persistence changed credentials")
	}
	stat, err := os.Stat(c.cookiePath)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("profile must retain private cookie-file permissions: %v", err)
	}
}

func TestUserProfileCacheDoesNotSurviveSessionReplacementOrLogout(t *testing.T) {
	c := profileClient(t, func(r *http.Request) (*http.Response, error) { return profileHTTPResponse(r, profileResponse), nil })
	if _, err := c.GetUserInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Even a caller passing the old profile along with new credentials cannot
	// accidentally associate it with the new session.
	newSession := c.GetCookies()
	newSession.SessData = "session-b"
	if err := c.SetCookies(newSession); err != nil {
		t.Fatal(err)
	}
	if c.GetCachedUserInfo() != nil {
		t.Fatal("new session inherited the old profile")
	}
	if err := c.ClearCookies(); err != nil {
		t.Fatal(err)
	}
	restarted := &Client{cookiePath: c.cookiePath}
	restarted.loadCookies()
	if info := restarted.GetCachedUserInfo(); info == nil || info.IsLogin {
		t.Fatal("logout must be known locally after restart")
	}
}

func TestUserProfileNetworkFailuresDoNotMeanLogout(t *testing.T) {
	for _, failure := range []string{"offline", "risk-control"} {
		t.Run(failure, func(t *testing.T) {
			c := profileClient(t, func(r *http.Request) (*http.Response, error) { return profileHTTPResponse(r, profileResponse), nil })
			if _, err := c.GetUserInfo(context.Background()); err != nil {
				t.Fatal(err)
			}
			c.httpClient.Transport = playurlTransport(func(r *http.Request) (*http.Response, error) {
				if failure == "offline" {
					return nil, errors.New("offline")
				}
				return profileHTTPResponse(r, `{"code":-352,"message":"temporarily unavailable"}`), nil
			})
			if _, err := c.GetUserInfo(context.Background()); err == nil {
				t.Fatal("unavailable account service must remain an error")
			}
			if info := c.GetCachedUserInfo(); info == nil || !info.IsLogin || info.Uname != "Account A" {
				t.Fatal("network failure erased the known profile")
			}
		})
	}
}

func TestConfirmedLoginExpiryReplacesCachedProfile(t *testing.T) {
	for _, body := range []string{`{"code":-101,"message":"not logged in"}`, `{"code":0,"data":{"isLogin":false}}`} {
		c := profileClient(t, func(r *http.Request) (*http.Response, error) { return profileHTTPResponse(r, profileResponse), nil })
		if _, err := c.GetUserInfo(context.Background()); err != nil {
			t.Fatal(err)
		}
		c.httpClient.Transport = playurlTransport(func(r *http.Request) (*http.Response, error) { return profileHTTPResponse(r, body), nil })
		if info, err := c.GetUserInfo(context.Background()); err != nil || info.IsLogin {
			t.Fatal("confirmed expiry must show the logged-out state")
		}
		restarted := &Client{cookiePath: c.cookiePath}
		restarted.loadCookies()
		if info := restarted.GetCachedUserInfo(); info == nil || info.IsLogin || info.Uname != "" {
			t.Fatal("expired account reappeared after restart")
		}
	}
}

func TestLateProfileResponseCannotRestoreLoggedOutOrReplacedAccount(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "logout", true: "replacement"}[replace], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{}, 1)
			defer close(release)
			c := profileClient(t, func(r *http.Request) (*http.Response, error) {
				close(entered)
				<-release
				return profileHTTPResponse(r, profileResponse), nil
			})
			result := make(chan error, 1)
			go func() { _, err := c.GetUserInfo(context.Background()); result <- err }()
			<-entered
			if replace {
				if err := c.SetCookies(&CookieData{SessData: "session-b"}); err != nil {
					t.Fatal(err)
				}
				c.cacheUserInfo("session-b", &UserInfo{IsLogin: true, Uname: "Account B"})
			} else if err := c.ClearCookies(); err != nil {
				t.Fatal(err)
			}
			release <- struct{}{}
			if err := <-result; err == nil {
				t.Fatal("late response from the old session was accepted")
			}
			info := c.GetCachedUserInfo()
			if info == nil || (!replace && info.IsLogin) || (replace && info.Uname != "Account B") {
				t.Fatal("late request restored the wrong account")
			}
		})
	}
}
