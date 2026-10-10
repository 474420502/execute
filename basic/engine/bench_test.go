package engine

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// benchHandler 是一个最轻量的处理器, 用于把测量重点放在执行器本身.
func benchHandler(items []int) error {
	sink.Add(int64(len(items)))
	return nil
}

var sink atomic.Int64

func benchEngine(b *testing.B, mode Mode, opts ...Option[int]) *Engine[int] {
	b.Helper()
	e, err := New(Config[int]{
		Handler:  benchHandler,
		Mode:     mode,
		Interval: time.Millisecond,
	}, opts...)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = e.Wait(nil) })
	return e
}

// BenchmarkCollectTrigger 测量触发模式下"投递 + 执行"的整体吞吐.
func BenchmarkCollectTrigger(b *testing.B) {
	e := benchEngine(b, ModeTrigger)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := e.Collect(i); err != nil {
			b.Fatal(err)
		}
	}
	_ = e.Flush()
}

// BenchmarkCollectTry 测量非阻塞投递的纯入队开销(不等待执行).
func BenchmarkCollectTry(b *testing.B) {
	e := benchEngine(b, ModeTrigger)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := e.TryCollect(i); err != nil {
			b.Fatal(err)
		}
	}
	_ = e.Flush()
}

// BenchmarkFlushBatch 测量按批次提交的性能: 每批 128 条.
func BenchmarkFlushBatch(b *testing.B) {
	const batch = 128
	e := benchEngine(b, ModeInterval, WithInterval[int](time.Hour)) // 关闭周期, 只用 Flush 驱动
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < batch; j++ {
			if err := e.TryCollect(j); err != nil {
				b.Fatal(err)
			}
		}
		if err := e.Flush(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkConcurrentThroughput 测量并发模式的批次吞吐(处理器极快).
func BenchmarkConcurrentThroughput(b *testing.B) {
	e := benchEngine(b, ModeConcurrent,
		WithInterval[int](time.Millisecond),
		WithConcurrency[int](8),
	)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := e.TryCollect(i); err != nil {
			b.Fatal(err)
		}
		if i%128 == 127 {
			if err := e.Flush(); err != nil {
				b.Fatal(err)
			}
		}
	}
	_ = e.Flush()
	_ = e.Wait(nil)
}

// BenchmarkHandlerOverhead 测量执行器自身的开销(处理器为空).
func BenchmarkHandlerOverhead(b *testing.B) {
	for _, n := range []int{1, 64, 1024} {
		b.Run(fmt.Sprintf("items=%d", n), func(b *testing.B) {
			e := benchEngine(b, ModeInterval, WithInterval[int](time.Hour))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for j := 0; j < n; j++ {
					e.TryCollect(j) //nolint:errcheck // 基准测试不关心丢弃
				}
				_ = e.Flush()
			}
		})
	}
}
