package engine

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/474420502/execute/basic"
)

// TestSoakRandomOperations 以随机顺序并发调用全部公开 API, 用于在 -race 下
// 暴露串行场景覆盖不到的交错问题(重配置 / 暂停 / 关闭 / 投递 / 刷写 混合)。
func TestSoakRandomOperations(t *testing.T) {
	modes := []Mode{ModeInterval, ModeCompensate, ModeConcurrent, ModeThreshold, ModeTrigger}

	for _, mode := range modes {
		t.Run(mode.String(), func(t *testing.T) {
			var (
				received  atomic.Int64
				published atomic.Int64
			)
			e, err := New(Config[int]{
				Handler: func(items []int) error {
					received.Add(int64(len(items)))
					if rand.Intn(50) == 0 {
						return errors.New("random failure")
					}
					if rand.Intn(80) == 0 {
						panic("random panic")
					}
					return nil
				},
				Mode:         mode,
				Interval:     50 * time.Microsecond,
				BatchSize:    8,
				Concurrency:  3,
				QueueSize:    64,
				Recover:      func(any) {},
				OnExecute:    func(basic.ExecuteEvent) {},
				OnDrop:       func(basic.DropEvent) {},
				Backpressure: BackpressureDrop,
			})
			if err != nil {
				t.Fatalf("New error: %v", err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			const workers = 6
			var wg sync.WaitGroup
			for w := range workers {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					r := rand.New(rand.NewSource(int64(w) + 1)) //nolint:gosec // 测试用
					for ctx.Err() == nil {
						switch r.Intn(10) {
						case 0:
							e.Configure(
								WithInterval[int](time.Duration(r.Intn(3)+1)*50*time.Microsecond),
								WithConcurrency[int](r.Intn(4)+1),
								WithBatchSize[int](r.Intn(16)+1),
								WithMaxPending[int](r.Intn(64)),
							)
						case 1:
							e.Pause()
						case 2:
							e.Resume()
						case 3:
							_ = e.Flush()
						case 4:
							if err := e.TryCollect(w*1000 + r.Intn(1000)); err == nil {
								published.Add(1)
							}
						case 5:
							_ = e.Stats()
						case 6:
							_ = e.Status()
						default:
							if err := e.TryCollect(w*1000 + r.Intn(1000)); err == nil {
								published.Add(1)
							}
						}
					}
				}(w)
			}

			time.Sleep(150 * time.Millisecond)
			cancel()
			wg.Wait()

			e.Close()
			e.Close() // 幂等
			wctx, wcancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer wcancel()
			if err := e.Wait(wctx); err != nil {
				t.Fatalf("Wait error: %v", err)
			}

			stats := e.Stats()
			if e.Status() != basic.StatusStopped {
				t.Fatalf("关闭后状态应为 stopped, 实际 %v", e.Status())
			}
			if got := stats.ReceivedItems + stats.DroppedItems; got < uint64(published.Load()) {
				t.Fatalf("接收+丢弃(%d) 不应少于成功投递数(%d)", got, published.Load())
			}
			if stats.ProcessedItems > stats.ReceivedItems {
				t.Fatalf("处理数不应超过接收数: %+v", stats)
			}
		})
	}
}

// TestSoakNotifyAfterCloseNeverPanics 反复关闭并投递, 保证错误路径稳定.
func TestSoakNotifyAfterCloseNeverPanics(t *testing.T) {
	for i := range 50 {
		e, err := New(Config[int]{
			Handler:  func([]int) error { return nil },
			Mode:     ModeTrigger,
			Interval: time.Millisecond,
		})
		if err != nil {
			t.Fatalf("New error: %v", err)
		}
		if i%2 == 0 {
			_ = e.Collect(i)
		}
		e.Close()
		if err := e.Collect(i); !errors.Is(err, basic.ErrClosed) {
			t.Fatalf("关闭后 Collect 应返回 ErrClosed, 实际 %v", err)
		}
		if err := e.Wait(nil); err != nil {
			t.Fatalf("Wait error: %v", err)
		}
	}
}
