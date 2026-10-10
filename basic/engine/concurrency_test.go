package engine

import (
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// TestConcurrentModeNeverExceedsLimit 直接用底层 API 验证并发上限:
// 每个节拍派发一个批次, 处理器阻塞在闸门上; 在闸门打开前, 进入处理器的批次数
// 绝不能超过 Concurrency.
func TestConcurrentModeNeverExceedsLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const concurrency = 3
		const batches = 6

		var (
			cur     atomic.Int64
			peak    atomic.Int64
			arrived atomic.Int64
		)
		gate := make(chan struct{})
		e, err := New(Config[int]{
			Handler: func(items []int) error {
				c := cur.Add(1)
				for {
					old := peak.Load()
					if c <= old || peak.CompareAndSwap(old, c) {
						break
					}
				}
				arrived.Add(1)
				<-gate
				cur.Add(-1)
				return nil
			},
			Mode:        ModeConcurrent,
			Interval:    10 * time.Millisecond,
			Concurrency: concurrency,
		})
		if err != nil {
			t.Fatalf("New error: %v", err)
		}
		t.Cleanup(func() {
			close(gate)
			_ = e.Wait(nil)
		})

		for i := range batches {
			if err := e.Collect(i); err != nil {
				t.Fatalf("Collect error: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
			if got := arrived.Load(); got > concurrency {
				t.Fatalf("并发上限 %d 被突破: 第 %d 个节拍后已有 %d 个批次进入 handler (在途计数器=%d)",
					concurrency, i, got, e.inFlight.Load())
			}
		}
		waitForCond(t, time.Second, "达到并发上限", func() bool { return arrived.Load() == concurrency })
		if peak.Load() != concurrency {
			t.Fatalf("并发峰值应为 %d, 实际 %d", concurrency, peak.Load())
		}
	})
}

func waitForCond(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待超时(%v): %s", timeout, desc)
}
