package instance

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// TestSingleInstanceLockConcurrency 测试高并发场景下系统排他锁确保有且仅有 1 个主实例成功
func TestSingleInstanceLockConcurrency(t *testing.T) {
	tmpDir := t.TempDir()

	const concurrency = 10
	var wg sync.WaitGroup
	var acquiredCount int64
	var rejectedCount int64
	var acquiredLocks []InstanceLock
	var lockMu sync.Mutex

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock, err := tryAcquireSystemLock(tmpDir)
			if err == nil && lock != nil {
				atomic.AddInt64(&acquiredCount, 1)
				lockMu.Lock()
				acquiredLocks = append(acquiredLocks, lock)
				lockMu.Unlock()
			} else if errors.Is(err, errAlreadyRunning) {
				atomic.AddInt64(&rejectedCount, 1)
			}
		}()
	}

	wg.Wait()

	if acquiredCount != 1 {
		t.Fatalf("预期只有 1 个协程能成功获得排他锁，实际成功获得数量: %d", acquiredCount)
	}
	if rejectedCount != concurrency-1 {
		t.Fatalf("预期有 %d 个协程被排他锁拒绝，实际拒绝数量: %d", concurrency-1, rejectedCount)
	}

	// 释放持有的锁后，后续尝试应能再次成功获取
	for _, l := range acquiredLocks {
		_ = l.Release()
	}

	secondLock, err := tryAcquireSystemLock(tmpDir)
	if err != nil || secondLock == nil {
		t.Fatalf("释放锁后再次尝试获取失败: %v", err)
	}
	_ = secondLock.Release()
}
