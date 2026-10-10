package threshold_test

import (
	"fmt"
	"sync"
	"time"

	"github.com/474420502/execute/batch/threshold"
)

// ExampleThresholdExecute 演示阈值执行器: 高流量按量切批, 低流量由周期兜底.
func ExampleThresholdExecute() {
	var (
		mu     sync.Mutex
		bySize []int
		byTick []int
	)

	// handler 同时作为数量阈值与周期兜底的默认处理器.
	exec := threshold.NewThresholdExecute[int](func(items []int) error {
		mu.Lock()
		bySize = append(bySize, items...)
		mu.Unlock()
		return nil
	}).
		WithBatchSize(3).
		WithPeriodic(50 * time.Millisecond).
		WithPeriodicHandler(func(items []int) error {
			mu.Lock()
			byTick = append(byTick, items...)
			mu.Unlock()
			return nil
		})
	defer func() { _ = exec.Wait(nil) }()

	// 前 3 条立即触发数量阈值.
	for i := 1; i <= 3; i++ {
		if err := exec.Collect(i); err != nil {
			fmt.Println("collect:", err)
			return
		}
	}
	if err := exec.Flush(); err != nil {
		fmt.Println("flush:", err)
		return
	}

	// 剩余不足阈值的数据由周期兜底处理.
	if err := exec.Collect(4); err != nil {
		fmt.Println("collect:", err)
		return
	}
	time.Sleep(120 * time.Millisecond)

	mu.Lock()
	fmt.Println("by size:", bySize)
	fmt.Println("by tick:", byTick)
	mu.Unlock()

	// Output:
	// by size: [1 2 3]
	// by tick: [4]
}
