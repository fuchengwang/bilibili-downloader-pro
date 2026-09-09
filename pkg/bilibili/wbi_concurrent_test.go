package bilibili

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestWbiSignConcurrentSafety(t *testing.T) {
	client := GetDefaultClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 模拟多协程并发签名
	baseParams := map[string]string{
		"avid": "2",
		"bvid": "BV1xx411c7mD",
		"cid":  "62131",
		"qn":   "127",
	}

	var wg sync.WaitGroup
	concurrentCount := 50

	for i := 0; i < concurrentCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// 验证传入相同或共享参数 map 签名时不发生 concurrent map writes panic
			_, _ = client.SignWbiParams(ctx, baseParams)
		}(i)
	}

	wg.Wait()
	t.Log("WBI 并发签名测试通过，未发生 map 并发冲突或 panic")
}
