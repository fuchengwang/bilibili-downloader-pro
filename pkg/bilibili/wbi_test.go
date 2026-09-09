package bilibili

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestExtractSubFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png", "7cd084941338484aae1ad9425b84077c"},
		{"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f1ac82b7187d68.jpeg", "4932caff0ff746eab6f1ac82b7187d68"},
		{"simple_name.png", "simple_name"},
		{"noextension", "noextension"},
	}

	for _, tc := range tests {
		got := extractSubFilename(tc.input)
		if got != tc.expected {
			t.Errorf("extractSubFilename(%q) = %q, expected %q", tc.input, got, tc.expected)
		}
	}
}

func TestSignWbiParams_OrderingAndSpecialChars(t *testing.T) {
	client := GetDefaultClient()
	client.mu.Lock()
	client.wbiMixin = "ea1ac281041241e79e777623e666a13f"
	client.wbiCached = time.Now()
	client.mu.Unlock()

	params := map[string]string{
		"foo":   "hello!world",
		"bar":   "test'*(value)",
		"bvid":  "BV1xx411c7mD",
		"aid":   "170001",
		"wts":   "1700000000",
	}

	signed, err := client.SignWbiParams(context.Background(), params)
	if err != nil {
		t.Fatalf("SignWbiParams failed: %v", err)
	}

	// 验证 w_rid 存在且不为空
	if !strings.Contains(signed, "w_rid=") {
		t.Fatalf("Signed query missing w_rid: %s", signed)
	}
	if !strings.Contains(signed, "wts=") {
		t.Fatalf("Signed query missing wts: %s", signed)
	}

	// 解析 query 参数
	values, err := url.ParseQuery(signed)
	if err != nil {
		t.Fatalf("ParseQuery failed on %s: %v", signed, err)
	}

	// 验证特殊字符已被正确过滤
	if values.Get("foo") != "helloworld" {
		t.Errorf("Special char '!' was not filtered: %s", values.Get("foo"))
	}
	if values.Get("bar") != "testvalue" {
		t.Errorf("Special chars ''*( )' were not filtered: %s", values.Get("bar"))
	}
	if values.Get("aid") != "170001" || values.Get("bvid") != "BV1xx411c7mD" {
		t.Errorf("Standard params corrupted: aid=%s, bvid=%s", values.Get("aid"), values.Get("bvid"))
	}

	// 独立按算法手动校验 MD5
	expectedQuery := "aid=170001&bar=testvalue&bvid=BV1xx411c7mD&foo=helloworld&wts=1700000000"
	expectedSum := md5.Sum([]byte(expectedQuery + "ea1ac281041241e79e777623e666a13f"))
	expectedWRid := hex.EncodeToString(expectedSum[:])

	if values.Get("w_rid") != expectedWRid {
		t.Errorf("w_rid mismatch: got %s, expected %s", values.Get("w_rid"), expectedWRid)
	}
}

func TestSignWbiParams_ConcurrentExecution(t *testing.T) {
	client := GetDefaultClient()
	client.mu.Lock()
	client.wbiMixin = "ea1ac281041241e79e777623e666a13f"
	client.wbiCached = time.Now()
	client.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			p := map[string]string{
				"cid":  "123456",
				"bvid": "BV1xx411c7mD",
				"qn":   "127",
			}
			res, err := client.SignWbiParams(context.Background(), p)
			if err != nil || !strings.Contains(res, "w_rid=") {
				t.Errorf("Concurrent SignWbiParams failed at index %d: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestSignWbiParams_RFC3986SpaceEncoding(t *testing.T) {
	client := GetDefaultClient()
	client.mu.Lock()
	client.wbiMixin = "ea1ac281041241e79e777623e666a13f"
	client.wbiCached = time.Now()
	client.mu.Unlock()

	params := map[string]string{
		"keyword": "hello world test",
		"wts":     "1700000000",
	}

	signed, err := client.SignWbiParams(context.Background(), params)
	if err != nil {
		t.Fatalf("SignWbiParams failed: %v", err)
	}

	// 核心断言：RFC 3986 标准规范要求空格必须编码为 %20，不得为 +
	if strings.Contains(signed, "keyword=hello+world+test") {
		t.Errorf("Signed query incorrectly encoded spaces with '+' instead of '%%20': %s", signed)
	}
	if !strings.Contains(signed, "keyword=hello%20world%20test") {
		t.Errorf("Signed query missing 'keyword=hello%%20world%%20test': %s", signed)
	}

	// 校验 MD5 签名是基于 RFC 3986 query 计算的
	expectedQuery := "keyword=hello%20world%20test&wts=1700000000"
	expectedSum := md5.Sum([]byte(expectedQuery + "ea1ac281041241e79e777623e666a13f"))
	expectedWRid := hex.EncodeToString(expectedSum[:])

	values, err := url.ParseQuery(signed)
	if err != nil {
		t.Fatalf("ParseQuery failed: %v", err)
	}
	if values.Get("w_rid") != expectedWRid {
		t.Errorf("w_rid mismatch: got %s, expected %s", values.Get("w_rid"), expectedWRid)
	}
}

