package bilibili

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCourseAuthorizationRequiresOfficialURIAndStaysPrivate(t *testing.T) {
	c := &Client{}
	for _, uri := range []string{"", "short", "bilidrm://invalid", "bilidrm://00112233"} {
		selection := &StreamSelection{courseDRM: &courseStreamDRM{uris: []string{uri}}}
		if _, err := c.ResolveCourseKeys(context.Background(), selection); err == nil {
			t.Fatalf("invalid official stream identifier accepted: %q", uri)
		}
	}
	selection := &StreamSelection{courseDRM: &courseStreamDRM{uris: []string{"private-identifier"}}}
	encoded, err := json.Marshal(selection)
	if err != nil || strings.Contains(string(encoded), "private-identifier") {
		t.Fatal("authorization payload exposed to serialized task/frontend data")
	}
	if keys, err := c.ResolveCourseKeys(context.Background(), &StreamSelection{}); err != nil || len(keys) != 0 {
		t.Fatal("normal media should not initiate classroom authorization")
	}
	if _, err := c.ResolveCourseKeys(context.Background(), &StreamSelection{courseDRM: &courseStreamDRM{}}); err == nil {
		t.Fatal("protected course without an official authorization identifier was accepted")
	}
}

func TestCoursePublicAssetsExcludeAccountCookieAndBoundResponse(t *testing.T) {
	c := playurlClient(t, strings.Repeat("x", 1025), func(r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("User-Agent") == "" {
			t.Error("account cookie leaked or player header missing")
		}
	})
	c.cookieData = &CookieData{SessData: "never-send-to-public-asset"}
	if _, err := c.coursePublicRequest(context.Background(), http.MethodGet, courseSDKURL, nil, 1024); err == nil {
		t.Fatal("oversized public asset was accepted")
	}
}
