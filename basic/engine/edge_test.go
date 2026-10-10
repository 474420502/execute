package engine

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/474420502/execute/basic"
)

// 边界1: 并发上限缩容到小于在途批次数, 不得误放行, 归还后必须收敛.
//
// 注意: 并发上限限制的是"在途批次数"而不是"数据条数" —— 多条数据可能合并成一批,
// 因此这里每投递一条就等待它进入 handler, 保证每个批次只含一条数据。
func TestEdgeShrinkBelowInflight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const limit = 4
		var cur, peak, batches atomic.Int64
		gate := make(chan struct{})
		e, err := New(Config[int]{
			Handler: func(items []int) error {
				c := cur.Add(1)
				for {
					o := peak.Load()
					if c <= o || peak.CompareAndSwap(o, c) {
						break
					}
				}
				batches.Add(1)
				<-gate
				cur.Add(-1)
				return nil
			},
			Mode:        ModeConcurrent,
			Interval:    10 * time.Millisecond,
			Concurrency: limit,
		})
		if err != nil {
			t.Fatal(err)
		}
		released := false
		release := func() {
			if !released {
				released = true
				close(gate)
			}
		}
		t.Cleanup(func() { release(); _ = e.Wait(nil) })

		for i := range limit {
			want := int64(i + 1)
			if err := e.Collect(i); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for batches.Load() < want && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
		}
		if got := batches.Load(); got != limit {
			t.Fatalf("应先跑满 %d 个并发批次, 实际 %d", limit, got)
		}

		// 缩容到 1: 已在途的批次不应被剥夺, 也不应再多派发.
		e.Configure(WithConcurrency[int](1))
		if err := e.Collect(99); err != nil {
			t.Fatal(err)
		}
		time.Sleep(60 * time.Millisecond)
		if got := batches.Load(); got != limit {
			t.Fatalf("缩容后不得再放行新批次, 实际已有 %d 个批次进入 handler", got)
		}
		if peak.Load() > limit {
			t.Fatalf("峰值不应超过原上限 %d, 实际 %d", limit, peak.Load())
		}

		release()
		if err := e.Wait(nil); err != nil {
			t.Fatal(err)
		}
		// 关闭后验收: 接收的数据一条都不能丢(第 5 批在收尾阶段被派发并完成).
		stats := e.Stats()
		if stats.ReceivedItems != limit+1 {
			t.Fatalf("应接收 %d 条, 实际 %d", limit+1, stats.ReceivedItems)
		}
		if stats.DroppedItems != 0 {
			t.Fatalf("关闭不应丢数据, 实际丢弃 %d", stats.DroppedItems)
		}
		if stats.ProcessedItems != limit+1 {
			t.Fatalf("应处理 %d 条, 实际 %d", limit+1, stats.ProcessedItems)
		}
	})
}

// 边界2: 暂停后 Wait/Close 仍必须把残留数据尽力执行, 且不丢不重.
func TestEdgePauseThenCloseDrains(t *testing.T) {
	var processed atomic.Int64
	e, err := New(Config[int]{
		Handler:  func(items []int) error { processed.Add(int64(len(items))); return nil },
		Mode:     ModeInterval,
		Interval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Collect(1); err != nil {
		t.Fatal(err)
	}
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	e.Pause()
	for i := 2; i <= 6; i++ {
		if err := e.Collect(i); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.Stats().ProcessedItems; got != 1 {
		t.Fatalf("暂停期间不得执行, 实际处理 %d", got)
	}
	if err := e.Wait(nil); err != nil {
		t.Fatal(err)
	}
	if got := processed.Load(); got != 6 {
		t.Fatalf("关闭时必须执行完残留数据: 期望 6, 实际 %d", got)
	}
}

// 边界3: Wait 超时后执行器仍可继续完成, 且不 panic.
func TestEdgeWaitTimeoutThenDone(t *testing.T) {
	gate := make(chan struct{})
	e, err := New(Config[int]{
		Handler: func(items []int) error { <-gate; return nil },
		Mode:    ModeTrigger,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Collect(1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := e.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应返回 DeadlineExceeded, 实际 %v", err)
	}
	close(gate)
	if err := e.Wait(nil); err != nil {
		t.Fatalf("最终必须完全停止: %v", err)
	}
	if e.Status() != basic.StatusStopped {
		t.Fatalf("状态应为 stopped, 实际 %v", e.Status())
	}
}

// 边界4: 处理器内再次投递(重入)不得死锁.
func TestEdgeReentrantCollect(t *testing.T) {
	var e *Engine[int]
	var once sync.Once
	done := make(chan struct{})
	e, err := New(Config[int]{
		Handler: func(items []int) error {
			once.Do(func() {
				for i := 100; i < 110; i++ {
					if err := e.TryCollect(i); err != nil {
						return
					}
				}
			})
			return nil
		},
		Mode:     ModeTrigger,
		Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(done)
		_ = e.Collect(1)
		_ = e.Flush()
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("处理器内重入投递导致死锁")
	}
	if err := e.Wait(nil); err != nil {
		t.Fatal(err)
	}
	if got := e.Stats().ProcessedItems; got < 11 {
		t.Fatalf("重入投递的数据也应被处理, 实际 %d", got)
	}
}

// 边界5: OnDrop/OnExecute 回调中再次调用公开 API 不得死锁.
func TestEdgeObserverReentrancy(t *testing.T) {
	var e *Engine[int]
	var err error
	e, err = New(Config[int]{
		Handler:  func([]int) error { return nil },
		Mode:     ModeTrigger,
		Interval: time.Millisecond,
		OnExecute: func(basic.ExecuteEvent) {
			_ = e.TryCollect(1) // 观测回调内投递
		},
		OnDrop: func(basic.DropEvent) {
			_ = e.Status()
			_ = e.Stats()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = e.TryCollect(i)
		}
		_ = e.Flush()
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("观测回调重入导致死锁")
	}
	if err := e.Wait(nil); err != nil {
		t.Fatal(err)
	}
}

// 边界6: 关闭时"已接收但尚未执行"的数据必须全部执行完, 不允许静默丢失.
//
// 这是优雅关闭的核心承诺。用 TryCollect 精确统计"成功进入执行器"的条数, 关闭后
// 必须满足:
//
//	接收数 == 处理数 + 丢弃数 + 仍在缓冲区中的条数
func TestEdgeCloseLosslessDrain(t *testing.T) {
	for _, mode := range []Mode{ModeInterval, ModeCompensate, ModeConcurrent, ModeThreshold, ModeTrigger} {
		t.Run(mode.String(), func(t *testing.T) {
			const total = 32
			gate := make(chan struct{})
			var enteredOnce sync.Once
			entered := make(chan struct{})

			e, err := New(Config[int]{
				Handler: func(items []int) error {
					enteredOnce.Do(func() { close(entered) })
					<-gate // 阻塞处理器, 制造稳定的积压
					return nil
				},
				Mode:        mode,
				Interval:    time.Millisecond, // 节拍很密: 尽快把数据从队列搬进缓冲区
				BatchSize:   8,
				Concurrency: 2,
			})
			if err != nil {
				t.Fatalf("New(%v) error: %v", mode, err)
			}

			var accepted uint64
			for i := range total {
				if err := e.TryCollect(i); err == nil {
					accepted++
				}
			}
			if accepted == 0 {
				t.Fatalf("[%v] 没有任何数据被接收", mode)
			}

			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatalf("[%v] 没有批次进入处理器", mode)
			}

			// 处理器仍然阻塞: 缓冲区里应仍有未处理的数据.
			e.Close()
			close(gate)
			if err := e.Wait(nil); err != nil {
				t.Fatalf("Wait error: %v", err)
			}

			stats := e.Stats()
			if stats.ReceivedItems != accepted {
				t.Fatalf("接收数应为 %d, 实际 %d", accepted, stats.ReceivedItems)
			}
			lost := int64(stats.ReceivedItems) - int64(stats.ProcessedItems) - int64(stats.DroppedItems) - int64(stats.PendingItems)
			if lost != 0 {
				t.Fatalf("有 %d 条数据既未处理、也未丢弃、也不在缓冲区: %+v", lost, stats)
			}
			if stats.DroppedItems != 0 {
				t.Fatalf("[%v] 该场景(阻塞背压)不应丢数据: %+v", mode, stats)
			}
			if stats.ProcessedItems != stats.ReceivedItems {
				t.Fatalf("[%v] 收尾后应处理完所有已接收数据: %+v", mode, stats)
			}
			if stats.PendingItems != 0 {
				t.Fatalf("[%v] 收尾后缓冲区应为空: %+v", mode, stats)
			}
		})
	}
}
