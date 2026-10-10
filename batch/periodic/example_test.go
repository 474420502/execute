package periodic_test

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/474420502/execute/batch/periodic"
)

// ExampleInterval 演示固定节奏执行器: 采集与执行解耦, 业务侧只负责 Collect.
func ExampleInterval() {
	var (
		mu   sync.Mutex
		got  []int
		done = make(chan struct{})
	)
	exec := periodic.NewInterval[int](func(items []int) error {
		mu.Lock()
		got = append(got, items...)
		mu.Unlock()
		if len(items) > 0 {
			select {
			case <-done:
			default:
				close(done)
			}
		}
		return nil
	}, 50*time.Millisecond)
	defer func() { _ = exec.Wait(nil) }()

	for i := 1; i <= 3; i++ {
		if err := exec.Collect(i); err != nil {
			fmt.Println("collect:", err)
			return
		}
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		fmt.Println("timeout")
		return
	}

	mu.Lock()
	fmt.Println("batch:", got)
	mu.Unlock()

	// 关闭是幂等的, 也可以在 defer 中安全重复调用.
	exec.Close()

	// Output:
	// batch: [1 2 3]
}

// ExampleCompensate 演示固定速率补偿: 批次耗时超过周期时不会漂移.
func ExampleCompensate() {
	exec := periodic.NewCompensate[int](func(items []int) error {
		time.Sleep(2 * time.Millisecond) // 模拟比周期更慢的处理
		return nil
	}, time.Millisecond).
		// 处理失败时统一上报, 执行循环不会被打断.
		WithRecover(func(ierr any) { fmt.Println("recovered:", ierr) }).
		// 可观测: 每次执行完成都会回调(务必保持轻量).
		WithOnExecute(func(ev periodic.ExecuteEvent) {
			if !ev.OK() {
				fmt.Println("execute failed:", ev.Err)
			}
		})
	defer func() { _ = exec.Wait(nil) }()

	for i := range 3 {
		if err := exec.Collect(i); err != nil {
			fmt.Println("collect:", err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	exec.Close()

	// 关闭后再采集会返回 ErrClosed, 而不会 panic.
	if err := exec.Collect(1); errors.Is(err, periodic.ErrClosed) {
		fmt.Println("closed as expected")
	}

	// Output:
	// closed as expected
}

// ExampleConcurrent 演示并发批量: 并发执行由 worker 池承担, 因而 Flush 返回后
// 可能仍有在途批次, 需要同步时请使用 Wait.
func ExampleConcurrent() {
	var (
		mu   sync.Mutex
		seen = map[int]bool{}
		done = make(chan struct{})
	)
	exec := periodic.NewConcurrent[int](func(items []int) error {
		mu.Lock()
		for _, it := range items {
			seen[it] = true
		}
		n := len(seen)
		mu.Unlock()
		if n == 5 {
			select {
			case <-done:
			default:
				close(done)
			}
		}
		return nil
	}, 20*time.Millisecond, 4)
	defer func() { _ = exec.Wait(nil) }()

	for i := range 5 {
		if err := exec.Collect(i); err != nil {
			fmt.Println("collect:", err)
		}
	}
	if err := exec.Flush(); err != nil { // 立即把缓冲区中的数据派发给 worker
		fmt.Println("flush:", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		fmt.Println("timeout")
		return
	}

	mu.Lock()
	fmt.Println("unique items:", len(seen))
	mu.Unlock()

	// Output:
	// unique items: 5
}
