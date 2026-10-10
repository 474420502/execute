package periodic_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/474420502/execute/basic"
	"github.com/474420502/execute/basic/engine"
	"github.com/474420502/execute/batch/periodic"
)

// TestIntervalBatchAtPeriod 在虚拟时钟下验证:
// 周期未到时数据不会被执行, 到点后整批执行一次, 且批次内保持投递顺序.
func TestIntervalBatchAtPeriod(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var (
			mu   sync.Mutex
			got  []int
			runs int
		)
		e := periodic.NewInterval[int](func(items []int) error {
			mu.Lock()
			runs++
			got = append(got, items...)
			mu.Unlock()
			return nil
		}, 10*time.Millisecond)
		t.Cleanup(func() { _ = e.Wait(nil) })

		if err := e.Collect(1); err != nil {
			t.Fatalf("Collect error: %v", err)
		}
		if err := e.Collect(2); err != nil {
			t.Fatalf("Collect error: %v", err)
		}

		synctest.Wait() // 数据已进入缓冲区, 周期还没到
		mu.Lock()
		if runs != 0 {
			mu.Unlock()
			t.Fatal("周期未到时不应执行")
		}
		mu.Unlock()

		time.Sleep(10 * time.Millisecond) // 虚拟时间前进一个周期
		synctest.Wait()

		mu.Lock()
		defer mu.Unlock()
		if runs != 1 {
			t.Fatalf("应执行 1 次, 实际 %d", runs)
		}
		if len(got) != 2 || got[0] != 1 || got[1] != 2 {
			t.Fatalf("批次内容/顺序错误: %v", got)
		}
	})
}

// TestIntervalIdleNoEmptyExecution 空闲周期不应产生空批次.
func TestIntervalIdleNoEmptyExecution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var runs int
		e := periodic.NewInterval[int](func(items []int) error {
			if len(items) == 0 {
				t.Error("不应执行空批次")
			}
			runs++
			return nil
		}, 5*time.Millisecond)
		t.Cleanup(func() { _ = e.Wait(nil) })

		e.Start()
		time.Sleep(100 * time.Millisecond) // 20 个空周期
		synctest.Wait()
		if runs != 0 {
			t.Fatalf("空闲时不应执行, 实际 %d 次", runs)
		}

		if err := e.Collect(7); err != nil {
			t.Fatalf("Collect error: %v", err)
		}
		time.Sleep(10 * time.Millisecond) // 跨过一个完整网格点
		synctest.Wait()
		if runs != 1 {
			t.Fatalf("恢复投递后应执行 1 次, 实际 %d", runs)
		}
	})
}

// TestCompensateKeepsRate 固定速率补偿: 当批次耗时超过周期时, 下一轮不应再额外
// 等待一个完整周期, 从而保证长期平均速率.
//
// 说明: synctest 的虚拟时钟只会在"所有 goroutine 都阻塞"时前进, 而并发 worker
// 会被 time.Sleep 阻塞, 因此这里用"等到 handler 上报完成后再推进时间"的方式驱动,
// 每一步都是确定性的。
func TestCompensateKeepsRate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			period = 10 * time.Millisecond
			work   = 30 * time.Millisecond
		)
		done := make(chan time.Time, 4)
		e := periodic.NewCompensate[int](func(items []int) error {
			time.Sleep(work)
			done <- time.Now()
			return nil
		}, period)
		t.Cleanup(func() { _ = e.Wait(nil) })

		var times []time.Time
		for range 3 {
			if err := e.Collect(1); err != nil {
				t.Fatalf("Collect error: %v", err)
			}
			select {
			case ts := <-done:
				times = append(times, ts)
			case <-time.After(time.Second):
				t.Fatalf("第 %d 次执行超时", len(times)+1)
			}
		}

		if len(times) != 3 {
			t.Fatalf("应执行 3 次, 实际 %d", len(times))
		}
		// 不变式: 当批次耗时(30ms)超过周期(10ms)时, 下一次执行必须在上一批结束的
		// 同时立刻开始, 也就是相邻两次间隔恰好等于 work(30ms)。若不补偿, 每批都会
		// 额外等待一个周期, 间隔将变成 work+period=40ms。
		for i := 1; i < len(times); i++ {
			gap := times[i].Sub(times[i-1])
			if gap != work {
				t.Fatalf("补偿失效: 第 %d->%d 次间隔 = %v, 期望 %v(work)", i, i+1, gap, work)
			}
		}
	})
}

// TestConcurrentBoundedParallelism 并发批量: 在途批次数不得超过配置的并发上限,
// 并且能真正跑到并发上限.
//
// 用"闸门 + 到达计数"驱动, 并且每一步都等待上一批真正进入 handler 后再投递下一批,
// 因此与"节拍边界如何切分批次"无关, 结果是确定性的:
//   - 前两批进入 handler 后卡在闸门上;
//   - 第三批数据到达时没有空闲 worker, dispatcher 必须在信号量上等待,
//     这段时间内 handler 的到达计数不得增加(这正是并发上限的证明);
//   - 放行后才出现第三个批次。
func TestConcurrentBoundedParallelism(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const concurrency = 2

		var (
			inFlight atomic.Int64
			peak     atomic.Int64
			arrived  atomic.Int64
		)
		gate := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(gate) }) }

		e := periodic.NewConcurrent[int](func(items []int) error {
			cur := inFlight.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			arrived.Add(1)
			<-gate
			inFlight.Add(-1)
			return nil
		}, 10*time.Millisecond, concurrency)
		t.Cleanup(func() {
			release()
			_ = e.Wait(nil)
		})

		// 每投递一条数据, 就等到它真正进入 handler 为止, 保证"一批一条".
		for i := range concurrency {
			want := int64(i + 1)
			if err := e.Collect(i); err != nil {
				t.Fatalf("Collect error: %v", err)
			}
			waitFor(t, time.Second, "批次进入 handler", func() bool { return arrived.Load() == want })
		}
		if got := inFlight.Load(); got != concurrency {
			t.Fatalf("在途批次应为 %d, 实际 %d", concurrency, got)
		}

		// 第三批数据: 没有空闲 worker, 必须在信号量上等待(不进入 handler).
		if err := e.Collect(99); err != nil {
			t.Fatalf("Collect error: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
		if got := arrived.Load(); got != concurrency {
			t.Fatalf("并发上限 %d 被突破: 已有 %d 个批次进入 handler", concurrency, got)
		}
		if got := e.Stats().DroppedItems; got != 0 {
			t.Fatalf("背压策略下不应丢数据, 实际丢弃 %d", got)
		}

		release() // 放行前两批; 第三批随即开始
		waitFor(t, time.Second, "第三批开始执行", func() bool { return arrived.Load() == concurrency+1 })
		if err := e.Wait(nil); err != nil {
			t.Fatalf("Wait error: %v", err)
		}

		if peak.Load() != concurrency {
			t.Fatalf("并发峰值应为 %d, 实际 %d", concurrency, peak.Load())
		}
		if got := e.Stats().ProcessedItems; got != 3 {
			t.Fatalf("应处理 3 条数据, 实际 %d", got)
		}
	})
}

// TestConcurrentDropMode 并发 + 丢弃策略: worker 繁忙时整批数据被丢弃并计数.
//
// 通过极小的队列容量 + 阻塞的 worker 确定性地制造"worker 繁忙"状态:
// 第一批占住唯一 worker, 后续批次在节拍到来时被丢弃。
func TestConcurrentDropMode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		var once sync.Once
		release := make(chan struct{})

		e := periodic.NewConcurrent[int](func(items []int) error {
			once.Do(func() { close(entered) })
			<-release // 占住 worker, 直到测试结束
			return nil
		}, 10*time.Millisecond, 1)
		e.Configure(engine.WithBackpressure[int](engine.BackpressureDrop))
		t.Cleanup(func() {
			close(release)
			_ = e.Wait(nil)
		})

		if err := e.Collect(1); err != nil {
			t.Fatalf("Collect error: %v", err)
		}
		<-entered // worker 已卡住

		if err := e.Collect(2); err != nil { // 落入缓冲区, 等待下一个节拍
			t.Fatalf("Collect error: %v", err)
		}

		deadline := time.Now().Add(time.Second)
		for e.Stats().DroppedItems == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if got := e.Stats().DroppedItems; got == 0 {
			t.Fatalf("worker 繁忙时应丢弃批次并计数: %+v", e.Stats())
		}
	})
}

// TestPeriodicHandlerErrorIsolated 批次失败不影响后续周期执行.
func TestPeriodicHandlerErrorIsolated(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls int
		e := periodic.NewInterval[int](func(items []int) error {
			calls++
			if calls == 1 {
				return errors.New("first batch failed")
			}
			return nil
		}, 10*time.Millisecond)
		t.Cleanup(func() { _ = e.Wait(nil) })

		if err := e.Collect(1); err != nil {
			t.Fatalf("Collect error: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
		synctest.Wait()

		if err := e.Collect(2); err != nil {
			t.Fatalf("Collect error: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
		synctest.Wait()

		stats := e.Stats()
		if stats.Executions != 2 || stats.FailedExecutions != 1 {
			t.Fatalf("指标异常: %+v", stats)
		}
	})
}

// TestCloseAndCollectAfterClose 关闭后采集必须返回 ErrClosed, 且 Wait 可重复调用.
func TestCloseAndCollectAfterClose(t *testing.T) {
	e := periodic.NewInterval[int](func([]int) error { return nil }, time.Millisecond)
	if err := e.Wait(nil); err != nil {
		t.Fatalf("Wait error: %v", err)
	}
	e.Close()
	if st := e.Status(); st != basic.StatusStopped {
		t.Fatalf("关闭后状态应为 stopped, 实际 %v", st)
	}
	if err := e.Collect(1); !errors.Is(err, basic.ErrClosed) {
		t.Fatalf("关闭后 Collect 应返回 ErrClosed, 实际 %v", err)
	}
	if err := e.Wait(context.Background()); err != nil {
		t.Fatalf("重复 Wait 应成功: %v", err)
	}
}

// TestWithPeriodicHotReload 运行期修改周期: 修改后按新周期执行(热更新).
func TestWithPeriodicHotReload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var (
			mu  sync.Mutex
			got []int
		)
		e := periodic.NewInterval[int](func(items []int) error {
			mu.Lock()
			got = append(got, items...)
			mu.Unlock()
			return nil
		}, time.Hour) // 初始周期极长, 不会自动触发
		t.Cleanup(func() { _ = e.Wait(nil) })

		if err := e.Collect(1); err != nil {
			t.Fatalf("Collect error: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
		synctest.Wait()
		mu.Lock()
		if len(got) != 0 {
			mu.Unlock()
			t.Fatal("长周期下不应执行")
		}
		mu.Unlock()

		e.WithPeriodic(5 * time.Millisecond) // 热更新
		waitFor(t, time.Second, "热更新后按新周期执行", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(got) == 1
		})

		mu.Lock()
		defer mu.Unlock()
		if got[0] != 1 {
			t.Fatalf("热更新后应按新周期执行, 实际 %v", got)
		}
	})
}

// TestConcurrentWithNumHotReload 并发上限支持运行期热更新.
func TestConcurrentWithNumHotReload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		done := make(chan int, 1)
		e := periodic.NewConcurrent[int](func(items []int) error {
			done <- len(items)
			return nil
		}, 5*time.Millisecond, 1)
		t.Cleanup(func() { _ = e.Wait(nil) })

		e.WithConcurrentNum(4) // 热更新并发上限
		for i := range 4 {
			if err := e.Collect(i); err != nil {
				t.Fatalf("Collect error: %v", err)
			}
		}
		time.Sleep(10 * time.Millisecond) // 跨过下一个网格点

		waitFor(t, time.Second, "热更新并发上限后执行", func() bool { return len(done) > 0 })
		if n := <-done; n != 4 {
			t.Fatalf("本批应包含 4 条数据, 实际 %d", n)
		}
	})
}

// TestHandlerOf 无返回值的便捷回调包装.
func TestHandlerOf(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var got []int
		e := periodic.NewInterval[int](periodic.HandlerOf(func(items []int) {
			got = append(got, items...)
		}), 5*time.Millisecond)
		t.Cleanup(func() { _ = e.Wait(nil) })

		if err := e.Collect(42); err != nil {
			t.Fatalf("Collect error: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
		synctest.Wait()
		if len(got) != 1 || got[0] != 42 {
			t.Fatalf("HandlerOf 未生效: %v", got)
		}
	})
}

// TestNewFromConfig 覆盖按配置创建: 模式、并发、队列容量、背压都生效.
func TestNewFromConfig(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var (
			mu   sync.Mutex
			got  []int
			done = make(chan struct{})
		)
		e := periodic.NewFromConfig(periodic.Config[int]{
			Handler: func(items []int) error {
				mu.Lock()
				got = append(got, items...)
				n := len(got)
				mu.Unlock()
				if n == 4 {
					close(done)
				}
				return nil
			},
			Mode:         periodic.ModeConcurrent,
			Interval:     5 * time.Millisecond,
			Concurrency:  2,
			QueueSize:    8, // 精细控制队列容量
			Backpressure: periodic.BackpressureBlock,
			Name:         "from-config",
		})
		t.Cleanup(func() { _ = e.Wait(nil) })

		for i := range 4 {
			if err := e.Collect(i); err != nil {
				t.Fatalf("Collect error: %v", err)
			}
		}
		time.Sleep(5 * time.Millisecond)
		synctest.Wait()

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("NewFromConfig 创建的执行器未执行")
		}
		mu.Lock()
		defer mu.Unlock()
		if len(got) != 4 {
			t.Fatalf("应处理 4 条, 实际 %v", got)
		}
	})
}

// TestNewFromConfigInvalid 非法配置必须在构造期就暴露, 而不是运行期静默出错.
func TestNewFromConfigInvalid(t *testing.T) {
	for name, cfg := range map[string]periodic.Config[int]{
		"nil-handler": {Interval: time.Second},
		"zero-period": {Handler: func([]int) error { return nil }},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("非法配置应 panic")
				}
			}()
			periodic.NewFromConfig(cfg)
		})
	}
}
