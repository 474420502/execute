// Package periodic 提供基于时间的周期执行器.
//
// 三种模式共享同一套采集与生命周期实现, 差异只在"何时执行"与"如何执行":
//
//	Interval   —— 固定节奏: 每 Interval 执行一次缓冲区中的数据(允许丢拍);
//	Compensate —— 固定速率补偿: 批次耗时超过 Interval 时下一轮不额外等待,
//	              使长期平均速率稳定在 1/Interval;
//	Concurrent —— 并发批量: 每 Interval 取走一批数据, 由最多 Concurrency 个
//	              worker 并发执行。
//
// 三者都满足"采集与执行解耦": 业务侧只调用 Collect, 不感知执行时机与并发度;
// 关闭后 Collect 返回 error 而不会 panic.
package periodic

import (
	"runtime"
	"time"

	"github.com/474420502/execute/basic"
	"github.com/474420502/execute/basic/engine"
)

// Handler 是周期执行器的业务回调. items 仅在下一次执行前有效, 需要保留请自行复制.
type Handler[ITEM any] = basic.Handler[ITEM]

// ExecuteEvent 是批次执行结果的观测事件(等价于 basic.ExecuteEvent).
type ExecuteEvent = basic.ExecuteEvent

// RecoverFunc 是 panic 恢复回调(等价于 basic.RecoverFunc).
type RecoverFunc = basic.RecoverFunc

// 与执行器生命周期相关的错误, 便于调用方用 errors.Is 判定.
var (
	// ErrClosed 表示执行器已关闭, 不再接收数据.
	ErrClosed = basic.ErrClosed
	// ErrQueueFull 表示非阻塞收集时队列已满.
	ErrQueueFull = basic.ErrQueueFull
)

// Func 是不返回错误的便捷回调形式, 可通过 HandlerOf 包装成 Handler.
type Func[ITEM any] func(items []ITEM)

// HandlerOf 把无返回值的回调包装成 Handler.
func HandlerOf[ITEM any](fn Func[ITEM]) Handler[ITEM] {
	if fn == nil {
		return nil
	}
	return func(items []ITEM) error {
		fn(items)
		return nil
	}
}

// Interval 是固定节奏执行器.
//
// 事件循环: 每到 Interval 就把缓冲区中的数据整体取出执行一次; 缓冲区为空时跳过,
// 因此空闲期间不会产生空执行.
type Interval[ITEM any] struct {
	*engine.Engine[ITEM]
}

// Compensate 是固定速率补偿执行器.
//
// 与 Interval 的区别: 若一次执行耗时超过 Interval, Interval 会因为节拍漂移而
// 降低长期速率; Compensate 会把下一次唤醒对齐到理论网格点上, 因此长期平均
// 速率稳定为 1/Interval(即"时间补偿").
type Compensate[ITEM any] struct {
	*engine.Engine[ITEM]
}

// Concurrent 是并发批量执行器.
//
// 每个节拍把缓冲区中的数据作为一个批次交给 worker 池并发执行; 在途批次数不超过
// Concurrency. 批次之间可能乱序完成, 批次内部保持原有顺序.
type Concurrent[ITEM any] struct {
	*engine.Engine[ITEM]
}

// 编译期契约检查: 三种执行器都必须满足 basic.Executor.
var (
	_ basic.Executor[int] = (*Interval[int])(nil)
	_ basic.Executor[int] = (*Compensate[int])(nil)
	_ basic.Executor[int] = (*Concurrent[int])(nil)
)

// NewInterval 创建固定节奏执行器.
//
// handler 接收本次批次; interval <= 0 时不会被节拍触发(仅能靠 Flush 或阈值, 通常不这样用).
func NewInterval[ITEM any](handler Handler[ITEM], interval time.Duration) *Interval[ITEM] {
	e := mustEngine(engine.ModeInterval, handler, interval)
	return setFinalizer(&Interval[ITEM]{Engine: e})
}

// NewCompensate 创建固定速率补偿执行器.
func NewCompensate[ITEM any](handler Handler[ITEM], interval time.Duration) *Compensate[ITEM] {
	e := mustEngine(engine.ModeCompensate, handler, interval)
	return setFinalizer(&Compensate[ITEM]{Engine: e})
}

// NewConcurrent 创建并发批量执行器.
//
// concurrency <= 0 时取 runtime.NumCPU().
func NewConcurrent[ITEM any](handler Handler[ITEM], interval time.Duration, concurrency int) *Concurrent[ITEM] {
	e := mustEngine(engine.ModeConcurrent, handler, interval,
		engine.WithConcurrency[ITEM](concurrency))
	return setFinalizer(&Concurrent[ITEM]{Engine: e})
}

func mustEngine[ITEM any](mode engine.Mode, handler Handler[ITEM], interval time.Duration, opts ...engine.Option[ITEM]) *engine.Engine[ITEM] {
	if handler == nil {
		panic("periodic: handler must not be nil")
	}
	all := make([]engine.Option[ITEM], 0, len(opts)+1)
	all = append(all, engine.WithInterval[ITEM](interval))
	all = append(all, opts...)

	e, err := engine.New(engine.Config[ITEM]{
		Handler:  handler,
		Mode:     mode,
		Interval: interval,
	}, all...)
	if err != nil {
		panic(err)
	}
	return e
}

func setFinalizer[T any](v T) T {
	runtime.SetFinalizer(any(v), func(any) {
		if closer, ok := any(v).(interface{ Close() }); ok {
			closer.Close()
		}
	})
	return v
}

// WithPeriodic 热更新执行周期, 在下一个网格点生效; 可安全地在运行期调用.
func (e *Interval[ITEM]) WithPeriodic(interval time.Duration) *Interval[ITEM] {
	e.Configure(engine.WithInterval[ITEM](interval))
	return e
}

// WithMaxPending 设置缓冲区积压上限(<=0 表示不限制), 运行期可热更新.
func (e *Interval[ITEM]) WithMaxPending(n int) *Interval[ITEM] {
	e.Configure(engine.WithMaxPending[ITEM](n))
	return e
}

// WithBackpressure 设置背压策略(阻塞或丢弃), 运行期可热更新.
func (e *Interval[ITEM]) WithBackpressure(bp engine.Backpressure) *Interval[ITEM] {
	e.Configure(engine.WithBackpressure[ITEM](bp))
	return e
}

// WithRecover 设置 panic 恢复回调.
func (e *Interval[ITEM]) WithRecover(fn basic.RecoverFunc) *Interval[ITEM] {
	e.Configure(engine.WithRecover[ITEM](fn))
	return e
}

// WithOnExecute 设置批次执行完成观测回调(同步调用, 需保持轻量).
func (e *Interval[ITEM]) WithOnExecute(fn func(basic.ExecuteEvent)) *Interval[ITEM] {
	e.Configure(engine.WithOnExecute[ITEM](fn))
	return e
}

// WithPeriodic 热更新执行周期, 在下一个网格点生效.
func (e *Compensate[ITEM]) WithPeriodic(interval time.Duration) *Compensate[ITEM] {
	e.Configure(engine.WithInterval[ITEM](interval))
	return e
}

// WithMaxPending 设置缓冲区积压上限(<=0 表示不限制), 运行期可热更新.
func (e *Compensate[ITEM]) WithMaxPending(n int) *Compensate[ITEM] {
	e.Configure(engine.WithMaxPending[ITEM](n))
	return e
}

// WithBackpressure 设置背压策略(阻塞或丢弃), 运行期可热更新.
func (e *Compensate[ITEM]) WithBackpressure(bp engine.Backpressure) *Compensate[ITEM] {
	e.Configure(engine.WithBackpressure[ITEM](bp))
	return e
}

// WithRecover 设置 panic 恢复回调.
func (e *Compensate[ITEM]) WithRecover(fn basic.RecoverFunc) *Compensate[ITEM] {
	e.Configure(engine.WithRecover[ITEM](fn))
	return e
}

// WithOnExecute 设置批次执行完成观测回调.
func (e *Compensate[ITEM]) WithOnExecute(fn func(basic.ExecuteEvent)) *Compensate[ITEM] {
	e.Configure(engine.WithOnExecute[ITEM](fn))
	return e
}

// WithPeriodic 热更新执行周期, 在下一个网格点生效.
func (e *Concurrent[ITEM]) WithPeriodic(interval time.Duration) *Concurrent[ITEM] {
	e.Configure(engine.WithInterval[ITEM](interval))
	return e
}

// WithConcurrentNum 热更新并发 worker 数量(下一批生效).
func (e *Concurrent[ITEM]) WithConcurrentNum(n int) *Concurrent[ITEM] {
	e.Configure(engine.WithConcurrency[ITEM](n))
	return e
}

// WithMaxPending 设置缓冲区积压上限(<=0 表示不限制), 运行期可热更新.
func (e *Concurrent[ITEM]) WithMaxPending(n int) *Concurrent[ITEM] {
	e.Configure(engine.WithMaxPending[ITEM](n))
	return e
}

// WithBackpressure 设置背压策略(阻塞或丢弃), 运行期可热更新.
func (e *Concurrent[ITEM]) WithBackpressure(bp engine.Backpressure) *Concurrent[ITEM] {
	e.Configure(engine.WithBackpressure[ITEM](bp))
	return e
}

// WithRecover 设置 panic 恢复回调.
func (e *Concurrent[ITEM]) WithRecover(fn basic.RecoverFunc) *Concurrent[ITEM] {
	e.Configure(engine.WithRecover[ITEM](fn))
	return e
}

// WithOnExecute 设置批次执行完成观测回调.
func (e *Concurrent[ITEM]) WithOnExecute(fn func(basic.ExecuteEvent)) *Concurrent[ITEM] {
	e.Configure(engine.WithOnExecute[ITEM](fn))
	return e
}
