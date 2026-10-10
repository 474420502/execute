package utils

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestOnceDoOnlyOnce(t *testing.T) {
	var (
		o     Once
		count atomic.Int64
		wg    sync.WaitGroup
	)
	const n = 64
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if o.Do(func() { count.Add(1) }) {
				count.Add(0)
			}
		}()
	}
	wg.Wait()

	if got := count.Load(); got != 1 {
		t.Fatalf("Once.Do 应只执行一次, 实际 %d 次", got)
	}
	if !o.Done() {
		t.Fatal("Once.Done 应为 true")
	}
}

func TestOnceDoReturnsWinner(t *testing.T) {
	var o Once
	wins := 0
	for range 5 {
		if o.Do(func() {}) {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("Only one caller should win, got %d", wins)
	}
}

func TestOnceReset(t *testing.T) {
	var (
		o     Once
		count int
	)
	o.Do(func() { count++ })
	if o.Do(func() { count++ }) {
		t.Fatal("second Do must not run")
	}
	o.Reset()
	if !o.Do(func() { count++ }) {
		t.Fatal("Do after Reset must run")
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

func TestOncePanicAllowsRetry(t *testing.T) {
	var o Once
	func() {
		defer func() { _ = recover() }()
		o.Do(func() { panic("boom") })
	}()
	if o.Done() {
		t.Fatal("panic 后不应标记完成")
	}
	ran := false
	if !o.Do(func() { ran = true }) {
		t.Fatal("panic 之后应允许重试")
	}
	if !ran {
		t.Fatal("重试函数未执行")
	}
}

func TestOnceNilFunc(t *testing.T) {
	var o Once
	if o.Do(nil) {
		t.Fatal("nil func should not be reported as executed")
	}
	if o.Done() {
		t.Fatal("nil func must not mark done")
	}
}

func TestOnceNoWaitSingleWinner(t *testing.T) {
	var (
		o     OnceNoWait
		count atomic.Int64
		wg    sync.WaitGroup
	)
	const n = 128
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			o.Do(func() { count.Add(1) })
		}()
	}
	close(start)
	wg.Wait()

	if got := count.Load(); got != 1 {
		t.Fatalf("OnceNoWait.Do 应只执行一次, 实际 %d 次", got)
	}
	if !o.Done() {
		t.Fatal("OnceNoWait.Done 应为 true")
	}
}

func TestOnceNoWaitReset(t *testing.T) {
	var o OnceNoWait
	first := o.Do(func() {})
	if !first {
		t.Fatal("first Do should win")
	}
	if o.Do(func() {}) {
		t.Fatal("second Do must not win")
	}
	o.Reset()
	if !o.Do(func() {}) {
		t.Fatal("Do after Reset should win again")
	}
}
