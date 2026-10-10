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

// DropEvent 是数据被丢弃时的观测事件(等价于 basic.DropEvent).
type DropEvent = basic.DropEvent

// Backpressure 是队列/并发槽位不足时的行为(阻塞或丢弃).
type Backpressure = engine.Backpressure

const (
	// BackpressureBlock 队列满时阻塞投递(默认, 不丢数据).
	BackpressureBlock = engine.BackpressureBlock
	// BackpressureDrop 队列满时丢弃并计数, 不阻塞调用方.
	BackpressureDrop = engine.BackpressureDrop
)

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

// Config 是阈值执行器的完整创建配置, 用于需要精细控制队列容量与背压的场景。
type Config[ITEM any] struct {
	// Handler 是默认处理器(同时作为数量阈值与周期兜底两条路径的默认值), 必填。
	Handler Handler[ITEM]
	// BatchSize 是数量阈值, <=0 时取默认值 128。
	BatchSize int
	// Periodic 是周期兜底间隔, <=0 时取默认值 100ms。
	Periodic time.Duration
	// SizeHandler 覆盖"达到数量阈值"路径的处理器(可选)。
	SizeHandler Handler[ITEM]
	// PeriodicHandler 覆盖"周期兜底"路径的处理器(可选)。
	PeriodicHandler Handler[ITEM]

	// QueueSize 是采集队列容量; <=0 时使用引擎默认值 65536。
	QueueSize int
	// BufferCap 是批次缓冲区初始容量; <=0 时自动推导。
	BufferCap int
	// Backpressure 决定队列满时的行为, 默认阻塞。
	Backpressure Backpressure
	// MaxPending 限制缓冲区积压上限, <=0 表示不限制。
	MaxPending int

	// Recover 在 Handler panic 时被调用(可选)。
	Recover RecoverFunc
	// OnExecute 在每次批次执行完成后同步回调(可选, 必须轻量)。
	OnExecute func(ExecuteEvent)
	// OnDrop 在每次丢弃数据时同步回调(可选, 必须轻量)。
	OnDrop func(DropEvent)
	// Name 用于日志与指标标识。
	Name string
}

// NewFromConfig 使用完整配置创建阈值执行器。
func NewFromConfig[ITEM any](cfg Config[ITEM]) *ThresholdExecute[ITEM] {
	if cfg.Handler == nil {
		panic("threshold: handler must not be nil")
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	periodic := cfg.Periodic
	if periodic <= 0 {
		periodic = defaultPeriodic
	}
	sizeDo := cfg.SizeHandler
	if sizeDo == nil {
		sizeDo = cfg.Handler
	}
	periodicDo := cfg.PeriodicHandler
	if periodicDo == nil {
		periodicDo = cfg.Handler
	}

	e, err := engine.New(engine.Config[ITEM]{
		Handler:         cfg.Handler,
		PeriodicHandler: periodicDo,
		SizeHandler:     sizeDo,
		Mode:            engine.ModeThreshold,
		Interval:        periodic,
		BatchSize:       batchSize,
		QueueSize:       cfg.QueueSize,
		BufferCap:       cfg.BufferCap,
		Backpressure:    engine.Backpressure(cfg.Backpressure),
		MaxPending:      cfg.MaxPending,
		Recover:         cfg.Recover,
		OnExecute:       cfg.OnExecute,
		OnDrop:          cfg.OnDrop,
		Name:            cfg.Name,
	})
	if err != nil {
		panic(err)
	}
	exec := &ThresholdExecute[ITEM]{Engine: e}
	runtime.SetFinalizer(exec, func(x *ThresholdExecute[ITEM]) { x.Close() })
	return exec
}

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

// WithBackpressure 设置背压策略(阻塞或丢弃), 运行期可热更新.
func (exec *ThresholdExecute[ITEM]) WithBackpressure(bp Backpressure) *ThresholdExecute[ITEM] {
	exec.Configure(engine.WithBackpressure[ITEM](bp))
	return exec
}

// WithMaxPending 设置缓冲区积压上限(<=0 表示不限制), 运行期可热更新.
func (exec *ThresholdExecute[ITEM]) WithMaxPending(n int) *ThresholdExecute[ITEM] {
	exec.Configure(engine.WithMaxPending[ITEM](n))
	return exec
}

// WithOnDrop 设置丢弃数据时的观测回调(同步调用, 需保持轻量).
func (exec *ThresholdExecute[ITEM]) WithOnDrop(fn func(DropEvent)) *ThresholdExecute[ITEM] {
	exec.Configure(engine.WithOnDrop[ITEM](fn))
	return exec
}

// WithRecover 设置 panic 恢复回调.
func (exec *ThresholdExecute[ITEM]) WithRecover(fn basic.RecoverFunc) *ThresholdExecute[ITEM] {
	exec.Configure(engine.WithRecover[ITEM](fn))
	return exec
}

// WithOnExecute 设置批次执行完成观测回调(同步调用, 需保持轻量).
func (exec *ThresholdExecute[ITEM]) WithOnExecute(fn func(ExecuteEvent)) *ThresholdExecute[ITEM] {
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
