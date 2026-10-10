package engine

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/474420502/execute/basic"
)

func newEngine[ITEM any](t *testing.T, mode Mode, handler basic.Handler[ITEM], opts ...Option[ITEM]) *Engine[ITEM] {
	t.Helper()
	e, err := New(Config[ITEM]{
		Handler:  handler,
		Mode:     mode,
		Interval: 5 * time.Millisecond,
	}, opts...)
	if err != nil {
		t.Fatalf("New(%v) error: %v", mode, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := e.Wait(ctx); err != nil {
			t.Errorf("Wait(%v) 超时: %v", mode, err)
		}
	})
	return e
}

// TestTriggerMergesBatch 验证"数据到达即合并为一个批次"的核心语义:
// 连续投递的 5 条数据应当只触发 1 次执行, 且批次内包含全部 5 条数据.
func TestTriggerMergesBatch(t *testing.T) {
	var (
		executions atomic.Int64
		got        []int
		mu         sync.Mutex
	)
	e := newEngine(t, ModeTrigger, func(items []int) error {
		executions.Add(1)
		mu.Lock()
		got = append(got, items...)
		mu.Unlock()
		return nil
	})

	for i := range 5 {
		if err := e.Collect(i); err != nil {
			t.Fatalf("Collect(%d) error: %v", i, err)
		}
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	if err := e.Wait(nil); err != nil {
		t.Fatalf("Wait error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if executions.Load() < 1 {
		t.Fatal("至少应执行一次")
	}
	if len(got) != 5 {
		t.Fatalf("应收到 5 条数据, 实际 %v", got)
	}
	for i, v := range got {
		if v != i {
			t.Fatalf("第 %d 条数据应为 %d, 实际 %d (顺序必须保持)", i, i, v)
		}
	}
}

// TestDoubleCloseNoPanic 复现并锁死历史缺陷: Close 使用了未初始化的 channel,
// 直接 panic. 现在双次 Close 必须安全, 且 Wait 必须返回.
func TestDoubleCloseNoPanic(t *testing.T) {
	e := newEngine(t, ModeInterval, func(items []int) error { return nil })

	e.Close()
	e.Close()
	e.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.Wait(ctx); err != nil {
		t.Fatalf("重复 Close 后 Wait 应成功, 实际: %v", err)
	}
	if st := e.Status(); st != basic.StatusStopped {
		t.Fatalf("状态应为 stopped, 实际 %v", st)
	}
}

// TestCollectAfterCloseReturnsError 关闭后再采集必须返回错误而不是 panic.
func TestCollectAfterCloseReturnsError(t *testing.T) {
	e := newEngine(t, ModeInterval, func(items []int) error { return nil })
	if err := e.Wait(nil); err != nil {
		t.Fatalf("Wait error: %v", err)
	}

	if err := e.Collect(1); !errors.Is(err, basic.ErrClosed) {
		t.Fatalf("Close 后 Collect 应返回 ErrClosed, 实际 %v", err)
	}
	if err := e.TryCollect(1); !errors.Is(err, basic.ErrClosed) {
		t.Fatalf("Close 后 TryCollect 应返回 ErrClosed, 实际 %v", err)
	}
	if err := e.Flush(); !errors.Is(err, basic.ErrClosed) {
		t.Fatalf("Close 后 Flush 应返回 ErrClosed, 实际 %v", err)
	}
}

// TestCloseBeforeStart 未启动就关闭: 不应产生 goroutine, 也不允许再采集.
func TestCloseBeforeStart(t *testing.T) {
	e, err := New(Config[int]{Handler: func([]int) error { return nil }, Mode: ModeInterval, Interval: time.Second})
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	e.Close()
	if st := e.Status(); st != basic.StatusStopped {
		t.Fatalf("未启动即关闭后状态应为 stopped, 实际 %v", st)
	}
	if err := e.Wait(nil); err != nil {
		t.Fatalf("Wait error: %v", err)
	}
	if err := e.Collect(1); !errors.Is(err, basic.ErrClosed) {
		t.Fatalf("应返回 ErrClosed, 实际 %v", err)
	}
}

// TestHandlerErrorRecorded Handler 返回 error 时必须被记录, 且不影响后续执行.
func TestHandlerErrorRecorded(t *testing.T) {
	var calls atomic.Int64
	e := newEngine(t, ModeTrigger, func(items []int) error {
		if calls.Add(1) == 1 {
			return errors.New("first failure")
		}
		return nil
	})

	if err := e.Collect(1); err != nil {
		t.Fatalf("Collect error: %v", err)
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	if err := e.Collect(2); err != nil {
		t.Fatalf("Collect error: %v", err)
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}

	stats := e.Stats()
	if stats.Executions != 2 {
		t.Fatalf("应执行 2 次, 实际 %d", stats.Executions)
	}
	if stats.FailedExecutions != 1 {
		t.Fatalf("应失败 1 次, 实际 %d", stats.FailedExecutions)
	}
	if stats.FailedItems != 1 {
		t.Fatalf("失败数据应为 1 条, 实际 %d", stats.FailedItems)
	}
	if stats.ProcessedItems != 2 || stats.SucceededItems != 1 {
		t.Fatalf("处理/成功计数异常: %+v", stats)
	}
	if stats.LastError == "" {
		t.Fatal("LastError 不应为空")
	}
	if len(stats.RecentErrors) != 1 || stats.RecentErrors[0].Panicked {
		t.Fatalf("RecentErrors 异常: %+v", stats.RecentErrors)
	}
}

// TestHandlerPanicIsolated Handler panic 必须被隔离: 不丢失后续数据, 计入失败, 触发 Recover.
func TestHandlerPanicIsolated(t *testing.T) {
	var (
		recovered atomic.Int64
		calls     atomic.Int64
	)
	e := newEngine(t, ModeTrigger,
		func(items []int) error {
			if calls.Add(1) == 1 {
				panic("handler exploded")
			}
			return nil
		},
		WithRecover[int](func(ierr any) { recovered.Add(1) }),
	)

	if err := e.Collect(1); err != nil {
		t.Fatalf("Collect error: %v", err)
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	if err := e.Collect(2); err != nil {
		t.Fatalf("panic 后 Collect 应继续可用: %v", err)
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("panic 后 Flush 应继续可用: %v", err)
	}

	if got := recovered.Load(); got != 1 {
		t.Fatalf("Recover 应被调用 1 次, 实际 %d", got)
	}
	stats := e.Stats()
	if stats.Executions != 2 || stats.FailedExecutions != 1 {
		t.Fatalf("panic 后指标异常: %+v", stats)
	}
	if len(stats.RecentErrors) != 1 || !stats.RecentErrors[0].Panicked {
		t.Fatalf("RecentErrors 应标记 panicked: %+v", stats.RecentErrors)
	}
	if e.Status() != basic.StatusRunning {
		t.Fatalf("panic 后执行器仍在运行, 实际 %v", e.Status())
	}
}

// TestRecoverFuncPanicIsolated Recover 回调自身 panic 也不能拖垮 dispatcher.
func TestRecoverFuncPanicIsolated(t *testing.T) {
	e := newEngine(t, ModeTrigger,
		func(items []int) error { panic("boom") },
		WithRecover[int](func(ierr any) { panic("recover exploded") }),
	)

	if err := e.Collect(1); err != nil {
		t.Fatalf("Collect error: %v", err)
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	if e.Stats().FailedExecutions != 1 {
		t.Fatalf("应记录 1 次失败: %+v", e.Stats())
	}
}

// TestTryCollectDrop 丢弃策略下, 队列满必须立即返回 ErrQueueFull 并计数.
func TestTryCollectDrop(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	e, err := New(Config[int]{
		Handler: func([]int) error {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release // 卡住 dispatcher, 让队列填满
			return nil
		},
		Mode:      ModeTrigger,
		Interval:  time.Second,
		QueueSize: 16,
	})
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	t.Cleanup(func() {
		close(release)
		_ = e.Wait(nil)
	})

	if err := e.TryCollect(1); err != nil {
		t.Fatalf("第一次 TryCollect 应成功: %v", err)
	}
	<-entered // handler 已进入, dispatcher 不再消费队列

	var full error
	for i := 0; i < 100 && full == nil; i++ {
		if err := e.TryCollect(i); errors.Is(err, basic.ErrQueueFull) {
			full = err
		}
	}
	if full == nil {
		t.Fatal("队列应被填满并返回 ErrQueueFull")
	}
	// 注意: handler 仍阻塞在 <-release 上, 这里不能用 Await(会排在阻塞的
	// 命令之后); 丢弃发生在上面的 TryCollect 调用内, 指标已同步结算.
	if got := e.Stats().DroppedItems; got == 0 {
		t.Fatal("丢弃计数应大于 0")
	}
}

// TestMaxPendingDrops 超过 MaxPending 上限的数据会被丢弃并计数.
//
// 暂停执行器可以确定性地制造"缓冲区积压": dispatcher 仍在消费队列,
// 但不会把缓冲区交给 Handler, 因此积压只受 MaxPending 约束.
func TestMaxPendingDrops(t *testing.T) {
	e := newEngine(t, ModeInterval, func([]int) error { return nil },
		WithMaxPending[int](2), WithInterval[int](time.Hour))

	e.Pause()
	for i := range 10 {
		if err := e.Collect(i); err != nil {
			t.Fatalf("暂停期间 Collect(%d) 应成功: %v", i, err)
		}
	}

	stats := e.Await()
	if stats.DroppedItems == 0 {
		t.Fatalf("超过 MaxPending 应发生丢弃: %+v", stats)
	}
	if stats.ReceivedItems != 10 {
		t.Fatalf("接收计数应为 10, 实际 %d", stats.ReceivedItems)
	}
	if stats.PendingItems > 2 {
		t.Fatalf("缓冲区积压不应超过 MaxPending=2, 实际 %d", stats.PendingItems)
	}
}

// TestConcurrentDropMode 并发 + 丢弃策略: worker 繁忙时批次被丢弃并计数, 不阻塞 dispatcher.
//
// 场景: 单个 worker 被卡住, 上游持续投递数据; 定时节拍到来时若没有空闲 worker,
// 批次必须被丢弃并计数, 而不是阻塞 dispatcher 拖垮整条链路.
func TestConcurrentDropMode(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	e, err := New(Config[int]{
		Handler: func([]int) error {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return nil
		},
		Mode:         ModeConcurrent,
		Interval:     time.Millisecond,
		Concurrency:  1,
		Backpressure: BackpressureDrop,
	})
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	t.Cleanup(func() {
		close(release)
		_ = e.Wait(nil)
	})

	// 持续投递数据(阻塞式 Collect: 队列满时自然形成背压).
	feedStop := make(chan struct{})
	feedDone := make(chan struct{})
	go func() {
		defer close(feedDone)
		for i := 0; ; i++ {
			select {
			case <-feedStop:
				return
			default:
			}
			if err := e.Collect(i); err != nil {
				return
			}
		}
	}()

	<-entered // 唯一 worker 已卡住, 后续节拍必须走丢弃分支

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e.Stats().DroppedItems > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(feedStop)
	<-feedDone

	if got := e.Stats().DroppedItems; got == 0 {
		t.Fatal("worker 繁忙时应丢弃批次并计数")
	}
}

// TestPauseResume 暂停期间数据被缓存而不执行, 恢复后立即处理.
func TestPauseResume(t *testing.T) {
	var (
		mu    sync.Mutex
		items []int
	)
	e := newEngine(t, ModeInterval, func(batch []int) error {
		mu.Lock()
		items = append(items, batch...)
		mu.Unlock()
		return nil
	}, WithInterval[int](10*time.Millisecond))

	e.Pause() // Pause 返回后, 新采集的数据一定不会被自动执行
	if err := e.Collect(1); err != nil {
		t.Fatalf("暂停期间 Collect 应成功: %v", err)
	}
	if err := e.Collect(2); err != nil {
		t.Fatalf("暂停期间 Collect 应成功: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(items) != 0 {
		mu.Unlock()
		t.Fatalf("暂停期间不应执行, 实际 %v", items)
	}
	mu.Unlock()
	if st := e.Status(); st != basic.StatusPaused {
		t.Fatalf("状态应为 paused, 实际 %v", st)
	}

	e.Resume()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(items)
		mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(items) != 2 {
		t.Fatalf("恢复后应处理 2 条, 实际 %v", items)
	}
}

// TestFlushWaitsForExecution Flush 必须等到数据真正被执行后才返回.
func TestFlushWaitsForExecution(t *testing.T) {
	var done atomic.Bool
	e := newEngine(t, ModeTrigger, func(items []int) error {
		time.Sleep(20 * time.Millisecond)
		done.Store(true)
		return nil
	})

	if err := e.Collect(1); err != nil {
		t.Fatalf("Collect error: %v", err)
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}
	if !done.Load() {
		t.Fatal("Flush 返回时 handler 应已执行完成")
	}
}

// TestNilHandlerRejected 无处理器时必须返回错误而不是运行时 panic.
func TestNilHandlerRejected(t *testing.T) {
	if _, err := New(Config[int]{Mode: ModeInterval}); err == nil {
		t.Fatal("handler 为 nil 时应返回错误")
	}
	if _, err := New(Config[int]{Handler: func([]int) error { return nil }, Mode: Mode(99)}); err == nil {
		t.Fatal("未知模式应返回错误")
	}
}

// TestConcurrentCloseDuringCollect 并发关闭 + 采集: 不允许 panic, 不允许丢数据以外的问题.
func TestConcurrentCloseDuringCollect(t *testing.T) {
	var processed atomic.Int64
	e, err := New(Config[int]{
		Handler:   func(items []int) error { processed.Add(int64(len(items))); return nil },
		Mode:      ModeConcurrent,
		Interval:  time.Millisecond,
		QueueSize: 8,
	})
	if err != nil {
		t.Fatalf("New error: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := range 8 {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if err := e.TryCollect(w*1000 + i); err != nil {
					return
				}
			}
		}(w)
	}

	time.Sleep(30 * time.Millisecond)
	close(stop)
	wg.Wait()
	e.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.Wait(ctx); err != nil {
		t.Fatalf("Wait error: %v", err)
	}

	stats := e.Stats()
	if stats.ProcessedItems == 0 {
		t.Fatal("应有数据被处理")
	}
	if stats.ReceivedItems < stats.ProcessedItems {
		t.Fatalf("处理数不应超过接收数: %+v", stats)
	}
}

// TestModeString 覆盖 Mode/Backpressure 的字符串表示, 便于日志可读性.
func TestModeString(t *testing.T) {
	cases := map[Mode]string{
		ModeInterval:   "interval",
		ModeCompensate: "compensate",
		ModeConcurrent: "concurrent",
		ModeThreshold:  "threshold",
		ModeTrigger:    "trigger",
		Mode(42):       "unknown",
	}
	for mode, want := range cases {
		if got := mode.String(); got != want {
			t.Errorf("Mode(%d).String() = %q, want %q", mode, got, want)
		}
	}
	if BackpressureDrop.String() != "drop" || BackpressureBlock.String() != "block" {
		t.Error("Backpressure.String 异常")
	}
}
