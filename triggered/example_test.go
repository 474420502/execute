package triggered_test

import (
	"fmt"
	"sync"

	"github.com/474420502/execute/triggered"
)

// ExampleRegisterExecute 演示事件触发执行器:
// 数据到达即执行(把当时缓冲区内的数据合成一个批次), 并可在批次之间共享状态。
func ExampleRegisterExecute() {
	var (
		mu       sync.Mutex
		batches  int
		received []int
	)
	done := make(chan struct{})

	exec := triggered.RegisterExecute[int](func(items *triggered.Items[int]) error {
		mu.Lock()
		batches++
		received = append(received, items.Value...)
		total, _ := items.Shared.Load().(int)
		items.Shared.Store(total + items.Len()) // 跨批次累计
		mu.Unlock()
		if items.Len() == 2 {
			select {
			case <-done:
			default:
				close(done)
			}
		}
		return nil
	}).WithShared(0)
	defer func() { _ = exec.Wait(nil) }()

	// Notify 把数据放入有界队列; 队列未满时不会阻塞.
	for i := 1; i <= 4; i++ {
		if err := exec.Notify(i); err != nil {
			fmt.Println("notify:", err)
			return
		}
	}
	if err := exec.Flush(); err != nil { // 等待所有已投递数据执行完成
		fmt.Println("flush:", err)
		return
	}

	mu.Lock()
	fmt.Println("batches:", batches)
	fmt.Println("received:", received)
	fmt.Println("shared:", exec.Shared().Load())
	mu.Unlock()

	// Output:
	// batches: 4
	// received: [1 2 3 4]
	// shared: 4
}

// ExampleRegisterExecuteEx 演示带配置的注册方式与优雅关闭.
func ExampleRegisterExecuteEx() {
	var count int

	exec := triggered.RegisterExecuteEx(&triggered.Config[int]{
		ItemsChanSize: 128, // Notify 队列容量
		Name:          "example",
		ExecuteDo: func(items *triggered.Items[int]) error {
			count += items.Len()
			return nil
		},
	})
	defer func() { _ = exec.Wait(nil) }()

	if err := exec.Notify(1); err != nil {
		fmt.Println("notify:", err)
	}
	if err := exec.Flush(); err != nil {
		fmt.Println("flush:", err)
	}

	exec.Close()
	exec.Close() // 幂等

	fmt.Println("count:", count)
	// 关闭之后投递会返回 ErrClosed, 而不是 panic.
	fmt.Println("after close:", exec.Notify(2))

	// Output:
	// count: 1
	// after close: execute: executor is closed
}
