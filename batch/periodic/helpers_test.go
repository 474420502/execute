package periodic_test

import (
	"testing"
	"time"
)

// waitFor 在虚拟时钟下轮询等待条件成立.
//
// testing/synctest.Wait() 只保证"其它 goroutine 此刻都处于阻塞状态", 并不能保证
// 异步工作(例如并发 worker)已经完成 —— 一个正在 time.Sleep 的 worker 也算阻塞。
// 因此凡是需要等待"异步结果是可见的"的场景, 都应该显式轮询。
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
