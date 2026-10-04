package bilibili

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const courseSDKURL = "https://s1.hdslb.com/bfs/static/player/main/widgets/npd.drm_sdk.7d8e1e5f.js"
const courseSDKDigest = "0d5f1fbb37844fc564bc00bbb165dfd34f79a98e9f3774074c78fce4ae74d6ba"

var courseSDKCache struct {
	sync.Mutex
	wasm []byte
}

// Created only after a successful full-content response from the classroom API.
// Neither the authorization payload nor the media keys are serialized to Wails
// or persisted with resumable tasks.
type courseStreamDRM struct{ uris []string }

// ResolveCourseKeys follows the official player's SPC/CKC authorization flow
// for the already selected, account-authorized classroom streams.
func (c *Client) ResolveCourseKeys(ctx context.Context, selection *StreamSelection) (map[string][]byte, error) {
	if selection == nil || selection.courseDRM == nil {
		return nil, nil
	}
	if len(selection.courseDRM.uris) == 0 {
		return nil, fmt.Errorf("该课堂媒体未提供可用的官方播放授权标识")
	}
	kids := make(map[string]bool)
	for _, uri := range selection.courseDRM.uris {
		separator := strings.LastIndex(uri, "//")
		if separator < 0 || len(uri)-separator-2 != 32 {
			return nil, fmt.Errorf("该课堂媒体未提供可用的官方播放授权标识")
		}
		kid := uri[separator+2:]
		decoded, err := hex.DecodeString(kid)
		if err != nil || len(decoded) != 16 {
			return nil, fmt.Errorf("该课堂媒体未提供可用的官方播放授权标识")
		}
		kids[strings.ToLower(kid)] = true
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	binary, err := c.courseSDK(ctx)
	if err != nil {
		return nil, err
	}
	cer, err := c.coursePublicRequest(ctx, http.MethodGet, "https://bvc-drm.bilivideo.com/cer/bilidrm_pub.key", nil, 10*1024)
	if err != nil {
		return nil, err
	}
	// Interpreter mode is portable and avoids executable memory/JIT entitlements
	// on macOS. The guest has no network, user filesystem, or environment access.
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter().WithCloseOnContextDone(true).WithMemoryLimitPages(1024))
	defer r.Close(context.Background())
	compiled, err := r.CompileModule(ctx, binary)
	if err != nil {
		return nil, fmt.Errorf("课堂播放授权模块无法加载")
	}
	if err := instantiateCourseHost(ctx, r, compiled); err != nil {
		return nil, fmt.Errorf("课堂播放授权模块初始化失败")
	}
	module, err := r.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithStartFunctions("w"))
	if err != nil {
		return nil, fmt.Errorf("课堂播放授权模块启动失败")
	}
	sdk := courseSDKInstance{module: module}
	keys := make(map[string][]byte)
	for kid := range kids {
		key, err := c.authorizeCourseKey(ctx, &sdk, kid, cer)
		if err != nil {
			for _, key := range keys {
				clear(key)
			}
			return nil, err
		}
		keys[kid] = key
	}
	return keys, nil
}

func (c *Client) courseSDK(ctx context.Context) ([]byte, error) {
	courseSDKCache.Lock()
	defer courseSDKCache.Unlock()
	if courseSDKCache.wasm != nil {
		return courseSDKCache.wasm, nil
	}
	script, err := c.coursePublicRequest(ctx, http.MethodGet, courseSDKURL, nil, 2*1024*1024)
	if err != nil {
		return nil, err
	}
	const prefix = "data:application/octet-stream;base64,"
	// The SDK also declares the bare data-URL prefix as a helper constant.
	// Locate the actual WASM magic rather than that empty prefix declaration.
	start := bytes.Index(script, []byte(prefix+"AGFzbQ"))
	if start < 0 {
		return nil, fmt.Errorf("课堂播放授权模块格式不受支持")
	}
	encoded := script[start+len(prefix):]
	end := bytes.IndexByte(encoded, '"')
	if end < 0 {
		return nil, fmt.Errorf("课堂播放授权模块不完整")
	}
	binary, err := base64.StdEncoding.DecodeString(string(encoded[:end]))
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(binary)) != courseSDKDigest {
		return nil, fmt.Errorf("课堂播放授权模块完整性校验失败，请更新应用后重试")
	}
	courseSDKCache.wasm = binary
	return binary, nil
}

// Public player assets and authorization exchange do not need account cookies.
// Account rights are checked by requestCheesePlayURL before this flow is reached.
func (c *Client) coursePublicRequest(ctx context.Context, method, endpoint string, body []byte, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("课堂播放授权请求无效")
	}
	req.Header.Set("User-Agent", BrowserUA)
	req.Header.Set("Referer", Referer)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("课堂播放授权服务连接失败，请稍后重试")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("课堂播放授权服务返回 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("课堂播放授权响应不完整或过大")
	}
	return data, nil
}

type courseSDKInstance struct{ module api.Module }

func (s *courseSDKInstance) alloc(ctx context.Context, data []byte) (uint64, error) {
	result, err := s.module.ExportedFunction("C").Call(ctx, uint64(len(data)))
	if err != nil || len(result) == 0 || result[0] == 0 || !s.module.Memory().Write(uint32(result[0]), data) {
		return 0, fmt.Errorf("课堂播放授权模块内存不足")
	}
	return result[0], nil
}

func (c *Client) authorizeCourseKey(ctx context.Context, sdk *courseSDKInstance, kid string, cer []byte) ([]byte, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, fmt.Errorf("课堂播放授权无法生成随机数据")
	}
	nonce := base64.RawURLEncoding.EncodeToString(random)[:16]
	clear(random)
	args := [][]byte{append([]byte(kid), 0), append([]byte(nonce), 0), cer, make([]byte, 4), make([]byte, 4)}
	var ptrs []uint64
	for _, data := range args {
		ptr, err := sdk.alloc(ctx, data)
		if err != nil {
			return nil, err
		}
		ptrs = append(ptrs, ptr)
	}
	status, err := sdk.module.ExportedFunction("x").Call(ctx, ptrs[0], ptrs[1], ptrs[2], uint64(len(cer)), ptrs[3], ptrs[4])
	if err != nil || len(status) == 0 || int32(status[0]) != 0 {
		return nil, fmt.Errorf("课堂播放授权请求生成失败")
	}
	spc, err := sdk.output(uint32(ptrs[3]), uint32(ptrs[4]), 64*1024)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]string{"spc": base64.StdEncoding.EncodeToString(spc)})
	response, err := c.coursePublicRequest(ctx, http.MethodPost, "https://bvc-drm.bilivideo.com/bilidrm", body, 256*1024)
	if err != nil {
		return nil, err
	}
	var license struct {
		Status int    `json:"status"`
		CKC    string `json:"ckc"`
	}
	if json.Unmarshal(response, &license) != nil || license.Status != 200 {
		return nil, fmt.Errorf("课堂播放授权服务拒绝请求，请确认课程播放权限后重试")
	}
	ckc, err := base64.StdEncoding.DecodeString(license.CKC)
	if err != nil || len(ckc) == 0 {
		return nil, fmt.Errorf("课堂播放授权响应格式不受支持")
	}
	ckcPtr, err := sdk.alloc(ctx, ckc)
	if err != nil {
		return nil, err
	}
	var outputs []uint64
	for i := 0; i < 4; i++ {
		ptr, err := sdk.alloc(ctx, make([]byte, 4))
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, ptr)
	}
	status, err = sdk.module.ExportedFunction("y").Call(ctx, ckcPtr, uint64(len(ckc)), ptrs[1], outputs[0], outputs[1], outputs[2], outputs[3])
	if err != nil || len(status) == 0 || int32(status[0]) != 0 {
		return nil, fmt.Errorf("课堂播放授权响应校验失败")
	}
	key, err := sdk.output(uint32(outputs[2]), uint32(outputs[3]), 16)
	if err != nil || len(key) != 16 {
		return nil, fmt.Errorf("课堂播放授权未返回有效媒体参数")
	}
	return key, nil
}

func (s *courseSDKInstance) output(address, lengthAddress uint32, maximum uint32) ([]byte, error) {
	ptr, ok := s.module.Memory().ReadUint32Le(address)
	n, lengthOK := s.module.Memory().ReadUint32Le(lengthAddress)
	if !ok || !lengthOK || n == 0 || n > maximum {
		return nil, fmt.Errorf("课堂播放授权模块返回无效数据")
	}
	data, ok := s.module.Memory().Read(ptr, n)
	if !ok {
		return nil, fmt.Errorf("课堂播放授权模块返回越界数据")
	}
	return append([]byte(nil), data...), nil
}
