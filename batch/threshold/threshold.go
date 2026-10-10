// Package threshold 提供"数据积累到阈值或周期到达时执行"的批量执行器.
//
// 触发条件:
//
//	数量阈值: 缓冲区数据条数达到 BatchSize 时立即执行, 使用数量阈值回调;
//	周期兜底: 每隔 Periodic 把不足阈值的数据也执行掉, 使用周期回调。
//
// 两个条件可同时启用: 高流量时按量切批(低延迟), 低流量时由周期兜底(不饿死)。
package threshold

import (
	"runtime"
	"time"

	"github.com/474420502/execute/basic"
	"github.com/474420502/execute/basic/engine"
)

// Handler 是阈值执行器的业务回调. items 仅在下一次执行前有效, 需要保留请自行复制.
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

// 默认配置, 与历史行为保持一致.
const (
	defaultBatchSize = 128
	defaultPeriodic  = 100 * time.Millisecond
)

// ThresholdExecute 是阈值执行器.
//
// 默认 batchSize=128、periodic=100ms. 首次 Collect 时自动启动执行循环。
type ThresholdExecute[ITEM any] struct {
	*engine.Engine[ITEM]
}

// 编译期契约检查.
var _ basic.Executor[int] = (*ThresholdExecute[int])(nil)

// NewThresholdExecute 创建阈值执行器.
//
// handler 接收达到数量阈值的批次(单个节拍可能包含多个批次)。
func NewThresholdExecute[ITEM any](handler Handler[ITEM]) *ThresholdExecute[ITEM] {
	if handler == nil {
		panic("threshold: handler must not be nil")
	}
	// Handler 同时作为"数量阈值"与"周期兜底"两条路径的默认处理器:
	// 两个可选回调都未设置时, 全部批次都由它处理.
	e, err := engine.New(engine.Config[ITEM]{
		Handler:         handler,
		PeriodicHandler: handler,
		SizeHandler:     handler,
		Mode:            engine.ModeThreshold,
		Interval:        defaultPeriodic,
		BatchSize:       defaultBatchSize,
	})
	if err != nil {
		panic(err)
	}
	exec := &ThresholdExecute[ITEM]{Engine: e}
	runtime.SetFinalizer(exec, func(x *ThresholdExecute[ITEM]) { x.Close() })
	return exec
}

// WithBatchSize 设置数量阈值, 运行期可热更新(下一批生效).
func (exec *ThresholdExecute[ITEM]) WithBatchSize(size int) *ThresholdExecute[ITEM] {
	exec.Configure(engine.WithBatchSize[ITEM](size))
	return exec
}

// WithPeriodic 设置周期兜底间隔, 运行期可热更新(下一个网格点生效).
func (exec *ThresholdExecute[ITEM]) WithPeriodic(interval time.Duration) *ThresholdExecute[ITEM] {
	exec.Configure(engine.WithInterval[ITEM](interval))
	return exec
}

// WithBatchSizeHandler 设置"达到数量阈值"时的专用回调.
func (exec *ThresholdExecute[ITEM]) WithBatchSizeHandler(handler Handler[ITEM]) *ThresholdExecute[ITEM] {
	exec.Configure(engine.WithSizeHandler[ITEM](handler))
	return exec
}

// WithPeriodicHandler 设置"周期兜底"时的专用回调.
func (exec *ThresholdExecute[ITEM]) WithPeriodicHandler(handler Handler[ITEM]) *ThresholdExecute[ITEM] {
	exec.Configure(engine.WithPeriodicHandler[ITEM](handler))
	return exec
}

// WithRecover 设置 panic 恢复回调.
func (exec *ThresholdExecute[ITEM]) WithRecover(fn basic.RecoverFunc) *ThresholdExecute[ITEM] {
	exec.Configure(engine.WithRecover[ITEM](fn))
	return exec
}

// WithOnExecute 设置批次执行完成观测回调(同步调用, 需保持轻量).
func (exec *ThresholdExecute[ITEM]) WithOnExecute(fn func(basic.ExecuteEvent)) *ThresholdExecute[ITEM] {
	exec.Configure(engine.WithOnExecute[ITEM](fn))
	return exec
}

// Stop 停止执行器并等待在途任务与残留数据执行完成.
//
// 语义等价于 exec.Close() + exec.Wait(nil), 保留该方法是为了兼容既有调用习惯;
// 新代码推荐直接使用被提升的 Close/Wait.
func (exec *ThresholdExecute[ITEM]) Stop() {
	_ = exec.Wait(nil)
}
