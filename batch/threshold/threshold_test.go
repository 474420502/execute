package threshold_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/474420502/execute/batch/threshold"
)

// waitFor 在虚拟时钟下轮询等待条件成立.
func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
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

// recorder 汇总一次批次执行, 用于断言"哪些数据、以哪个处理器被执行".
type recorder struct {
	mu      sync.Mutex
	viaSize []int
	viaTick []int
}

func (r *recorder) size(items []int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.viaSize = append(r.viaSize, items...)
	return nil
}

func (r *recorder) tick(items []int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.viaTick = append(r.viaTick, items...)
	return nil
}

func (r *recorder) snapshot() (size, tick []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.viaSize...), append([]int(nil), r.viaTick...)
}

// TestBatchSizeTriggersImmediately 达到数量阈值必须立即执行, 不等周期.
func TestBatchSizeTriggersImmediately(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var rec recorder
		e := threshold.NewThresholdExecute[int](rec.size).
			WithBatchSize(3).
			WithPeriodic(time.Hour) // 周期极长, 只有数量阈值会触发
		t.Cleanup(func() { _ = e.Wait(nil) })

		for i := range 3 {
			if err := e.Collect(i); err != nil {
				t.Fatalf("Collect error: %v", err)
			}
		}

		waitFor(t, time.Second, "达到阈值立即执行", func() bool {
			size, _ := rec.snapshot()
			return len(size) == 3
		})
		size, tick := rec.snapshot()
		if len(tick) != 0 {
			t.Fatalf("周期兜底不应被触发, 实际 %v", tick)
		}
		for i, v := range size {
			if v != i {
				t.Fatalf("批次内容/顺序错误: %v", size)
			}
		}
		if got := e.Stats().Executions; got != 1 {
			t.Fatalf("应执行 1 次, 实际 %d", got)
		}
	})
}

// TestPeriodicFallbackFlushesPartialBatch 不足阈值的数据由周期兜底执行.
func TestPeriodicFallbackFlushesPartialBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var rec recorder
		e := threshold.NewThresholdExecute[int](rec.size).
			WithBatchSize(100).
			WithPeriodic(10 * time.Millisecond).
			WithPeriodicHandler(rec.tick)
		t.Cleanup(func() { _ = e.Wait(nil) })

		for i := range 3 {
			if err := e.Collect(i); err != nil {
				t.Fatalf("Collect error: %v", err)
			}
		}

		waitFor(t, time.Second, "周期兜底执行残留数据", func() bool {
			_, tick := rec.snapshot()
			return len(tick) == 3
		})
		size, tick := rec.snapshot()
		if len(size) != 0 {
			t.Fatalf("未达阈值不应走数量阈值处理器, 实际 %v", size)
		}
		if len(tick) != 3 {
			t.Fatalf("周期处理器应收到 3 条, 实际 %v", tick)
		}
	})
}

// TestBothHandlers 高流量走数量阈值处理器, 低流量走周期处理器.
func TestBothHandlers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var rec recorder
		e := threshold.NewThresholdExecute[int](rec.size).
			WithBatchSize(2).
			WithPeriodic(20 * time.Millisecond).
			WithPeriodicHandler(rec.tick)
		t.Cleanup(func() { _ = e.Wait(nil) })

		// 4 条 -> 触发两次数量阈值
		for i := range 4 {
			if err := e.Collect(i); err != nil {
				t.Fatalf("Collect error: %v", err)
			}
		}
		waitFor(t, time.Second, "两次数量阈值执行", func() bool {
			size, _ := rec.snapshot()
			return len(size) == 4
		})

		// 再补 1 条 -> 由周期兜底
		if err := e.Collect(99); err != nil {
			t.Fatalf("Collect error: %v", err)
		}
		waitFor(t, 2*time.Second, "周期兜底执行", func() bool {
			_, tick := rec.snapshot()
			return len(tick) == 1
		})

		size, tick := rec.snapshot()
		if len(size) != 4 {
			t.Fatalf("数量阈值处理器应收 4 条, 实际 %v", size)
		}
		if len(tick) != 1 || tick[0] != 99 {
			t.Fatalf("周期处理器应收 [99], 实际 %v", tick)
		}
	})
}

// TestCollectNeverDeadlocksWithSlowHandler 复现历史缺陷: Collect 在持锁状态下
// 向无缓冲信号通道发送, 当消费端忙时会永久死锁。
//
// 现在 Collect 必须始终可推进: 慢处理器只会把 dispatcher 卡住, 使队列填满后
// 让采集方承受背压, 而不会出现"谁都走不动"的自锁。
func TestCollectNeverDeadlocksWithSlowHandler(t *testing.T) {
	gate := make(chan struct{})
	var batches atomic.Int64
	e := threshold.NewThresholdExecute[int](func(items []int) error {
		batches.Add(1)
		<-gate // 每次处理都很慢
		return nil
	}).WithBatchSize(1).WithPeriodic(time.Hour)
	t.Cleanup(func() {
		close(gate)
		_ = e.Wait(nil)
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 1000 {
			if err := e.Collect(i); err != nil {
				return
			}
		}
	}()

	// 等待至少一个批次真正进入处理器(不能只等采集循环结束).
	deadline := time.Now().Add(5 * time.Second)
	for batches.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if batches.Load() == 0 {
		t.Fatal("慢处理器下没有任何批次被执行")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Collect 死锁: 消费端忙碌时无法继续采集")
	}
}

// TestCloseThenCollectReturnsError 关闭后采集返回错误且不 panic.
func TestCloseThenCollectReturnsError(t *testing.T) {
	e := threshold.NewThresholdExecute[int](func([]int) error { return nil })
	if err := e.Wait(nil); err != nil {
		t.Fatalf("Wait error: %v", err)
	}
	if err := e.Collect(1); err == nil {
		t.Fatal("关闭后 Collect 应返回错误")
	}
	if err := e.Collect(2); err == nil {
		t.Fatal("关闭后重复 Collect 应返回错误")
	}
	// 历史 API 兼容: Stop 必须幂等且可重复调用.
	e.Stop()
	e.Stop()
}

// TestHandlerPanicIsolated 处理器 panic 不得中断后续批次.
func TestHandlerPanicIsolated(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64
		e := threshold.NewThresholdExecute[int](func(items []int) error {
			if calls.Add(1) == 1 {
				panic("threshold handler boom")
			}
			return nil
		}).WithBatchSize(1).WithPeriodic(time.Hour)
		t.Cleanup(func() { _ = e.Wait(nil) })

		if err := e.Collect(1); err != nil {
			t.Fatalf("Collect error: %v", err)
		}
		waitFor(t, time.Second, "第一次执行失败被记录", func() bool {
			return e.Stats().FailedExecutions == 1
		})
		if err := e.Collect(2); err != nil {
			t.Fatalf("panic 后 Collect 应继续可用: %v", err)
		}
		waitFor(t, time.Second, "第二次执行成功", func() bool {
			return e.Stats().Executions == 2
		})
		if e.Status().String() != "running" {
			t.Fatalf("panic 后执行器应继续运行, 实际 %v", e.Status())
		}
	})
}

// TestHotReloadBatchSize 运行期调整阈值立即生效.
func TestHotReloadBatchSize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var rec recorder
		e := threshold.NewThresholdExecute[int](rec.size).
			WithBatchSize(1000).
			WithPeriodic(time.Hour)
		t.Cleanup(func() { _ = e.Wait(nil) })

		if err := e.Collect(1); err != nil {
			t.Fatalf("Collect error: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
		if size, _ := rec.snapshot(); len(size) != 0 {
			t.Fatalf("高阈值下不应执行, 实际 %v", size)
		}

		e.WithBatchSize(1)                   // 热更新
		if err := e.Collect(2); err != nil { // 新阈值在下一批数据上判定
			t.Fatalf("Collect error: %v", err)
		}
		waitFor(t, time.Second, "阈值热更新后立即生效", func() bool {
			size, _ := rec.snapshot()
			return len(size) == 2
		})
	})
}

// TestFlushExecutesPending 显式 Flush 会把残留数据执行掉.
func TestFlushExecutesPending(t *testing.T) {
	var rec recorder
	e := threshold.NewThresholdExecute[int](rec.size).
		WithBatchSize(10).
		WithPeriodic(time.Hour)
	t.Cleanup(func() { _ = e.Wait(nil) })

	if err := e.Collect(7); err != nil {
		t.Fatalf("Collect error: %v", err)
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	if size, _ := rec.snapshot(); len(size) != 1 || size[0] != 7 {
		t.Fatalf("Flush 应执行残留数据, 实际 %v", size)
	}
}

// TestHandlerOf 无返回值回调包装.
func TestHandlerOf(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var got atomic.Int64
		e := threshold.NewThresholdExecute[int](threshold.HandlerOf(func(items []int) {
			got.Add(int64(len(items)))
		})).WithBatchSize(2).WithPeriodic(time.Hour)
		t.Cleanup(func() { _ = e.Wait(nil) })

		for i := range 2 {
			if err := e.Collect(i); err != nil {
				t.Fatalf("Collect error: %v", err)
			}
		}
		waitFor(t, time.Second, "HandlerOf 生效", func() bool { return got.Load() == 2 })
	})
}
