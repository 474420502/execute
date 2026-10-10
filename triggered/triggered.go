// Package triggered 提供被动触发式执行器.
//
// 与 batch/periodic、batch/threshold 不同, 它不使用任何定时器:
// 每次 Notify 数据到达时, 立即把"当前缓冲区中已有的全部数据"合并成一个批次执行,
// 因此天然具备合并突发流量(批量放大)的能力, 适合事件驱动、日志聚合、通知推送等场景.
//
// 执行函数是串行的: 同一时刻只有一个批次在执行, 上游可以放心地用 Notify 做背压。
package triggered

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/474420502/execute/basic"
	"github.com/474420502/execute/basic/engine"
)

// Handler 是触发式执行器的业务回调.
//
// items.Value 仅在本批次的执行期间有效: 需要跨批次保留数据时请复制或保存
// Shared 中的引用。items.Shared 是从执行器创建起就存在、跨批次共享的状态载体。
type Handler[ITEM any] func(items *Items[ITEM]) error

// defaultChanSize 是 Notify 队列的默认容量.
const defaultChanSize = 1024

// Share 是跨批次共享的状态载体, 可安全地被多个 goroutine 并发访问.
//
// 存取值有两种风格, 按需选择:
//
//	原子风格: Load/Store/CompareAndSwap/Swap —— 适合标量配置、开关、版本号;
//	回调风格: Value/SetValue —— 与历史 API 兼容, 在锁内访问复合结构.
type Share struct {
	// v 保存当前值; 使用 atomic.Pointer 保证读写的原子性与可见性.
	v atomic.Pointer[any]

	// mu 保护回调风格的复合结构访问, 以及 v 尚未设置时的初值.
	mu     sync.Mutex
	valAny any
}

// NewShare 创建一个共享状态载体.
func NewShare() *Share { return &Share{} }

// Store 原子地设置共享值.
func (s *Share) Store(value any) {
	if s == nil {
		return
	}
	s.v.Store(&value)
}

// Load 原子地读取共享值; 从未设置过时返回 nil.
func (s *Share) Load() any {
	if s == nil {
		return nil
	}
	if p := s.v.Load(); p != nil {
		return *p
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.valAny
}

// CompareAndSwap 原子比较并交换; 仅当当前值 == old 时写入 new 并返回 true.
func (s *Share) CompareAndSwap(old, new any) bool {
	if s == nil {
		return false
	}
	if p := s.v.Load(); p != nil {
		if *p != old {
			return false
		}
		return s.v.CompareAndSwap(p, &new)
	}

	// 尚未通过 Store 设置过: 退化为加锁比较.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.v.Load() != nil || s.valAny != old {
		return false
	}
	s.valAny = new
	s.v.Store(&new)
	return true
}

// Swap 原子地写入新值并返回旧值.
func (s *Share) Swap(value any) any {
	if s == nil {
		return nil
	}
	old := s.Load()
	s.Store(value)
	return old
}

// SetValue 设置共享值(回调风格, 与历史 API 兼容).
func (s *Share) SetValue(v any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.valAny = v
	s.mu.Unlock()
	s.v.Store(&v)
}

// Value 读取共享值并交给回调 do 处理(回调风格).
//
// 语义: do 在锁内被调用, 因此可以安全地读取复合结构; 但 do 中不得再次调用
// Value/SetValue(会自锁), 需要嵌套访问请改用 Load/Store。
func (s *Share) Value(do func(v any)) {
	if s == nil || do == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	do(s.valAny)
}

// Items 是一次执行的上下文: 本批次数据 + 跨批次共享状态.
type Items[T any] struct {
	// Value 是本批次的数据, 仅在本次执行期间有效.
	Value []T
	// Shared 是跨批次共享状态, 永不为 nil.
	Shared *Share

	seq uint64
}

// Len 返回本批次数据条数.
func (it *Items[T]) Len() int { return len(it.Value) }

// Seq 返回本批次的序号(从 1 开始), 便于幂等与排障.
func (it *Items[T]) Seq() uint64 { return it.seq }

// EventExecute 是被动触发式执行器.
//
// 生命周期: 创建即开始接收 Notify, 首次数据到达即执行;
// Close 幂等, Close 之后 Notify 返回 basic.ErrClosed(err 不再 panic).
type EventExecute[ITEM any] struct {
	*engine.Engine[ITEM]
	shared *Share
	seq    atomic.Uint64
}

// 编译期契约检查.
var _ basic.Executor[int] = (*EventExecute[int])(nil)

// Config 是 RegisterExecuteEx 的完整配置.
type Config[ITEM any] struct {
	// ItemsChanSize 是 Notify 队列容量; <=0 时取默认值 1024.
	ItemsChanSize int
	// QueueSize 是执行器采集队列容量; <=0 时与 ItemsChanSize 一致.
	QueueSize int
	// ExecuteDo 是业务回调, 必填.
	ExecuteDo Handler[ITEM]
	// Shared 是初始共享状态; 为 nil 时自动创建.
	Shared *Share
	// Recover 是 panic 恢复回调(可选).
	Recover basic.RecoverFunc
	// Backpressure 决定队列满时 Notify 的行为, 默认 BackpressureBlock.
	Backpressure engine.Backpressure
	// MaxPending 限制缓冲区积压; <=0 不限制.
	MaxPending int
	// Name 用于日志与指标标识.
	Name string
}

// RegisterExecute 注册一个执行单元.
//
// 等价于 RegisterExecuteEx(&Config{ExecuteDo: execDo})。
func RegisterExecute[ITEM any](execDo Handler[ITEM]) *EventExecute[ITEM] {
	return RegisterExecuteEx(&Config[ITEM]{ExecuteDo: execDo})
}

// RegisterExecuteEx 使用完整配置注册执行单元.
func RegisterExecuteEx[ITEM any](cfg *Config[ITEM]) *EventExecute[ITEM] {
	if cfg == nil || cfg.ExecuteDo == nil {
		panic("triggered: ExecuteDo must not be nil")
	}
	chanSize := cfg.ItemsChanSize
	if chanSize <= 0 {
		chanSize = defaultChanSize
	}
	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = chanSize
	}
	shared := cfg.Shared
	if shared == nil {
		shared = NewShare()
	}

	exec := &EventExecute[ITEM]{shared: shared}
	handler := basic.Handler[ITEM](func(items []ITEM) error {
		return cfg.ExecuteDo(&Items[ITEM]{
			Value:  items,
			Shared: exec.shared,
			seq:    exec.seq.Add(1),
		})
	})

	e, err := engine.New(engine.Config[ITEM]{
		Handler:      handler,
		Mode:         engine.ModeTrigger,
		Interval:     time.Hour, // 被动触发模式不使用节拍, 该值仅作占位
		QueueSize:    queueSize,
		BufferCap:    chanSize,
		Recover:      cfg.Recover,
		Backpressure: cfg.Backpressure,
		MaxPending:   cfg.MaxPending,
		Name:         cfg.Name,
	})
	if err != nil {
		panic(err)
	}
	exec.Engine = e

	runtime.SetFinalizer(exec, func(x *EventExecute[ITEM]) { x.Close() })
	return exec
}

// Shared 返回该执行器的共享状态载体.
func (exec *EventExecute[ITEM]) Shared() *Share { return exec.shared }

// WithShared 设置共享状态, 并返回自身以便链式调用.
func (exec *EventExecute[ITEM]) WithShared(v any) *EventExecute[ITEM] {
	exec.shared.SetValue(v)
	return exec
}

// WithRecover 设置 panic 恢复回调.
func (exec *EventExecute[ITEM]) WithRecover(fn basic.RecoverFunc) *EventExecute[ITEM] {
	exec.Configure(engine.WithRecover[ITEM](fn))
	return exec
}

// WithOnExecute 设置批次执行完成观测回调(同步调用, 需保持轻量).
func (exec *EventExecute[ITEM]) WithOnExecute(fn func(basic.ExecuteEvent)) *EventExecute[ITEM] {
	exec.Configure(engine.WithOnExecute[ITEM](fn))
	return exec
}

// Notify 投递一条事件. 队列满时按背压策略阻塞, 执行器已关闭时返回 basic.ErrClosed.
//
// 与历史行为一致: 突发投递的多条事件会被合并为一个批次执行。
func (exec *EventExecute[ITEM]) Notify(item ITEM) error {
	return exec.Collect(item)
}

// TryNotify 非阻塞投递; 队列满时返回 basic.ErrQueueFull 并计入丢弃指标.
func (exec *EventExecute[ITEM]) TryNotify(item ITEM) error {
	return exec.TryCollect(item)
}
