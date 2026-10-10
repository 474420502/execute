package basic

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSemaphoreLimitAndRelease 验证信号量不会超发 permit, 也不会丢失 permit.
func TestSemaphoreLimitAndRelease(t *testing.T) {
	const capacity = 4
	sem := NewSemaphore(capacity)

	var (
		cur    atomic.Int64
		peak   atomic.Int64
		active atomic.Int64
	)
	release := make(chan struct{})
	var wg sync.WaitGroup

	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !sem.AcquireWait() {
				return
			}
			c := cur.Add(1)
			active.Add(1)
			for {
				old := peak.Load()
				if c <= old || peak.CompareAndSwap(old, c) {
					break
				}
			}
			<-release
			active.Add(-1)
			cur.Add(-1)
			sem.Release()
		}()
	}

	time.Sleep(50 * time.Millisecond)
	if got := active.Load(); got != capacity {
		t.Fatalf("应恰好有 %d 个持有者, 实际 %d", capacity, got)
	}
	if got := peak.Load(); got > capacity {
		t.Fatalf("并发上限 %d 被突破, 峰值 %d", capacity, got)
	}
	close(release)
	wg.Wait()

	if got := peak.Load(); got != capacity {
		t.Fatalf("应达到并发上限 %d, 实际峰值 %d", capacity, got)
	}
	if sem.TryAcquire() != true {
		t.Fatal("所有 permit 归还后应能再次获取")
	}
	sem.Release()
}

// TestSemaphoreAcquireContextCancel 取消等待不应留下幽灵 permit.
func TestSemaphoreAcquireContextCancel(t *testing.T) {
	sem := NewSemaphore(1)
	if !sem.TryAcquire() {
		t.Fatal("首次获取应成功")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// 等待会一直阻塞, 直到下面的 Release.
		if !sem.AcquireWait() {
			return
		}
		sem.Release()
	}()

	time.Sleep(10 * time.Millisecond)
	sem.Release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("等待者未被唤醒")
	}

	// 现在信号量应恢复为"容量 1、占用 0".
	if !sem.TryAcquire() {
		t.Fatal("唤醒并归还后应可再次获取")
	}
	sem.Release()
}

// TestSemaphoreResize 缩容与扩容必须保持已占用数量不变.
func TestSemaphoreResize(t *testing.T) {
	sem := NewSemaphore(8)
	if got := sem.Cap(); got != 8 {
		t.Fatalf("初始容量应为 8, 实际 %d", got)
	}
	if got := sem.Resize(2); got != 2 {
		t.Fatalf("空闲时缩容到 2 应成功, 实际容量 %d", got)
	}
	if got := sem.InUse(); got != 0 {
		t.Fatalf("缩容后占用数应为 0, 实际 %d", got)
	}
	if got := sem.Resize(5); got != 5 {
		t.Fatalf("扩容到 5 应成功, 实际容量 %d", got)
	}
	if got := sem.Available(); got != 5 {
		t.Fatalf("扩容后应全部空闲, 实际 %d", got)
	}
}
