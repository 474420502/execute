package triggered_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/474420502/execute/basic"
	"github.com/474420502/execute/triggered"
)

// waitFor 轮询等待条件成立(triggered 使用真实时钟, 因此保留真实超时).
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

// TestFlushMergesBurst Flush 会把缓冲区中的积压数据合并成一个批次执行, 顺序不变.
//
// 触发模式的默认语义是"到达即执行"; 当调用方希望主动把一批数据合并提交时,
// 连续投递后调用 Flush 即可得到单批次执行(常见于批处理或测试场景)。
func TestFlushMergesBurst(t *testing.T) {
	var (
		mu       sync.Mutex
		received []int
		batches  int
	)

	exec := triggered.RegisterExecute[int](func(items *triggered.Items[int]) error {
		mu.Lock()
		batches++
		received = append(received, items.Value...)
		mu.Unlock()
		return nil
	})
	t.Cleanup(func() { _ = exec.Wait(nil) })

	for i := range 5 {
		if err := exec.Notify(i); err != nil {
			t.Fatalf("Notify(%d) error: %v", i, err)
		}
	}
	if err := exec.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 5 {
		t.Fatalf("应收到 5 条数据, 实际 %v", received)
	}
	for i, v := range received {
		if v != i {
			t.Fatalf("顺序必须保持: 第 %d 条应为 %d, 实际 %d", i, i, v)
		}
	}
	if batches < 1 {
		t.Fatal("应至少执行一个批次")
	}
}

// TestExecutionIsSerial 执行必须串行: 同一时刻只有一个批次在处理器中.
func TestExecutionIsSerial(t *testing.T) {
	var (
		running atomic.Int64
		peak    atomic.Int64
		batches atomic.Int64
	)
	exec := triggered.RegisterExecute[int](func(items *triggered.Items[int]) error {
		cur := running.Add(1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		batches.Add(1)
		time.Sleep(2 * time.Millisecond)
		running.Add(-1)
		return nil
	})
	t.Cleanup(func() { _ = exec.Wait(nil) })

	const producers = 4
	const perProducer = 50
	var wg sync.WaitGroup
	for p := range producers {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := range perProducer {
				if err := exec.Notify(p*1000 + i); err != nil {
					return
				}
			}
		}(p)
	}
	wg.Wait()
	if err := exec.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}

	waitFor(t, 3*time.Second, "所有数据被处理", func() bool {
		return exec.Stats().ProcessedItems == producers*perProducer
	})
	if peak.Load() != 1 {
		t.Fatalf("执行必须串行, 实际并发峰值 %d", peak.Load())
	}
	if batches.Load() == 0 {
		t.Fatal("应至少执行一个批次")
	}
}

// TestSharedStateAcrossBatches 跨批次共享状态可读写, 且并发下安全.
func TestSharedStateAcrossBatches(t *testing.T) {
	exec := triggered.RegisterExecute[int](func(items *triggered.Items[int]) error {
		// 原子风格读写: 回调内部不会重复获取共享锁, 适合在处理器里做累加.
		total, _ := items.Shared.Load().(int)
		items.Shared.Store(total + len(items.Value))
		return nil
	}).WithShared(0)
	t.Cleanup(func() { _ = exec.Wait(nil) })

	for i := range 100 {
		if err := exec.Notify(i); err != nil {
			t.Fatalf("Notify error: %v", err)
		}
	}
	if err := exec.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	waitFor(t, 3*time.Second, "全部数据被处理", func() bool {
		return exec.Stats().ProcessedItems == 100
	})
	if got := exec.Shared().Load(); got != 100 {
		t.Fatalf("共享状态累计应为 100, 实际 %v", got)
	}
}

// TestShareAtomicAPI Share 的原子读写接口.
func TestShareAtomicAPI(t *testing.T) {
	s := triggered.NewShare()

	if got := s.Load(); got != nil {
		t.Fatalf("初始值应为 nil, 实际 %v", got)
	}
	s.Store("v1")
	if got := s.Load(); got != "v1" {
		t.Fatalf("Load 应为 v1, 实际 %v", got)
	}
	if !s.CompareAndSwap("v1", "v2") {
		t.Fatal("CompareAndSwap 应成功")
	}
	if s.CompareAndSwap("v1", "v3") {
		t.Fatal("旧值不匹配时 CompareAndSwap 必须失败")
	}
	if got := s.Swap("v4"); got != "v2" {
		t.Fatalf("Swap 应返回旧值 v2, 实际 %v", got)
	}
	if got := s.Load(); got != "v4" {
		t.Fatalf("Swap 后应为 v4, 实际 %v", got)
	}

	// 并发读写不得触发 race detector.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 200 {
				_ = s.Load()
				s.Store(i*1000 + j)
				s.Value(func(any) {})
			}
		}(i)
	}
	wg.Wait()
}

// TestBatchSeqIsMonotonic 批次序号从 1 开始且严格递增.
func TestBatchSeqIsMonotonic(t *testing.T) {
	var (
		mu   sync.Mutex
		seqs []uint64
	)
	exec := triggered.RegisterExecute[int](func(items *triggered.Items[int]) error {
		mu.Lock()
		seqs = append(seqs, items.Seq())
		mu.Unlock()
		return nil
	})
	t.Cleanup(func() { _ = exec.Wait(nil) })

	for i := range 5 {
		if err := exec.Notify(i); err != nil {
			t.Fatalf("Notify error: %v", err)
		}
		if err := exec.Flush(); err != nil {
			t.Fatalf("Flush error: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seqs) == 0 {
		t.Fatal("应有批次执行")
	}
	if seqs[0] != 1 {
		t.Fatalf("批次序号应从 1 开始, 实际 %d", seqs[0])
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("批次序号必须递增: %v", seqs)
		}
	}
}

// TestCloseIsIdempotentAndNotifyReturnsError 复现历史缺陷: Close 使用了未初始化的
// channel 导致 panic。现在 Close 幂等, 且 Close 之后 Notify 返回错误而不是 panic。
func TestCloseIsIdempotentAndNotifyReturnsError(t *testing.T) {
	exec := triggered.RegisterExecute[int](func(items *triggered.Items[int]) error { return nil })

	if err := exec.Notify(1); err != nil {
		t.Fatalf("Notify error: %v", err)
	}
	if err := exec.Wait(nil); err != nil {
		t.Fatalf("Wait error: %v", err)
	}
	exec.Close()
	exec.Close()
	exec.Close()

	if st := exec.Status(); st != basic.StatusStopped {
		t.Fatalf("关闭后状态应为 stopped, 实际 %v", st)
	}
	if err := exec.Notify(2); !errors.Is(err, basic.ErrClosed) {
		t.Fatalf("关闭后 Notify 应返回 ErrClosed, 实际 %v", err)
	}
	if err := exec.TryNotify(3); !errors.Is(err, basic.ErrClosed) {
		t.Fatalf("关闭后 TryNotify 应返回 ErrClosed, 实际 %v", err)
	}
}

// TestHandlerPanicIsolated 处理器 panic 不得影响后续批次.
func TestHandlerPanicIsolated(t *testing.T) {
	var (
		calls atomic.Int64
		recov atomic.Int64
	)
	exec := triggered.RegisterExecute[int](func(items *triggered.Items[int]) error {
		if calls.Add(1) == 1 {
			panic("event handler boom")
		}
		return nil
	}).WithRecover(func(ierr any) { recov.Add(1) })
	t.Cleanup(func() { _ = exec.Wait(nil) })

	if err := exec.Notify(1); err != nil {
		t.Fatalf("Notify error: %v", err)
	}
	if err := exec.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	waitFor(t, 2*time.Second, "panic 被记录", func() bool {
		return exec.Stats().FailedExecutions == 1
	})
	if recov.Load() != 1 {
		t.Fatalf("Recover 应被调用一次, 实际 %d", recov.Load())
	}

	if err := exec.Notify(2); err != nil {
		t.Fatalf("panic 后 Notify 应继续可用: %v", err)
	}
	if err := exec.Flush(); err != nil {
		t.Fatalf("panic 后 Flush 应继续可用: %v", err)
	}
	if exec.Stats().Executions != 2 {
		t.Fatalf("应执行 2 个批次, 实际 %+v", exec.Stats())
	}
}

// TestConcurrentNotifyWithDropBackpressure 高并发投递 + 丢弃策略: 不得 panic 或死锁,
// 且指标自洽.
func TestConcurrentNotifyWithDropBackpressure(t *testing.T) {
	exec := triggered.RegisterExecuteEx(&triggered.Config[int]{
		ItemsChanSize: 4,
		ExecuteDo: func(items *triggered.Items[int]) error {
			time.Sleep(time.Millisecond)
			return nil
		},
	})
	t.Cleanup(func() { _ = exec.Wait(nil) })

	var (
		wg      sync.WaitGroup
		dropped atomic.Int64
	)
	for p := range 8 {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := range 200 {
				if err := exec.TryNotify(p*1000 + i); err != nil {
					dropped.Add(1)
				}
			}
		}(p)
	}
	wg.Wait()
	if err := exec.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	waitFor(t, 5*time.Second, "处理完成", func() bool {
		return exec.Stats().InFlight == 0
	})

	stats := exec.Stats()
	if stats.ReceivedItems == 0 {
		t.Fatal("应有数据被接收")
	}
	if stats.ReceivedItems+stats.DroppedItems != 1600 {
		t.Fatalf("接收 + 丢弃应等于投递总数 1600: %+v", stats)
	}
}

// TestRegisterExecuteExConfig 覆盖 RegisterExecuteEx 的配置项.
func TestRegisterExecuteExConfig(t *testing.T) {
	var (
		gotName atomic.Bool
	)
	exec := triggered.RegisterExecuteEx(&triggered.Config[int]{
		ItemsChanSize: 8,
		Name:          "unit-test",
		ExecuteDo: func(items *triggered.Items[int]) error {
			gotName.Store(true)
			return nil
		},
	})
	t.Cleanup(func() { _ = exec.Wait(nil) })

	if err := exec.Notify(1); err != nil {
		t.Fatalf("Notify error: %v", err)
	}
	if err := exec.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	if !gotName.Load() {
		t.Fatal("处理器未被调用")
	}
	if exec.Shared() == nil {
		t.Fatal("Shared 必须自动创建")
	}
}

// TestNilHandlerPanics 明确记录"缺少处理器"是编程错误(构造期 panic).
func TestNilHandlerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("ExecuteDo 为 nil 时应 panic")
		}
	}()
	triggered.RegisterExecute[int](nil)
}
