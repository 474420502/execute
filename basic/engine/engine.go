// Package engine 实现所有执行器共享的执行引擎.
//
// 引擎是"采集与执行解耦"的落点:
//   - Collect/Notify 只负责把数据放入有界队列(可配置阻塞或丢弃);
//   - 唯一的 dispatcher goroutine 拥有全部可变状态, 热路径上不需要互斥锁;
//   - 到点(或达到阈值)时把批次交给 Handler, 并按需限制并发;
//   - Close 幂等; Wait 可同步等待在途任务与残留数据全部结束;
//   - 周期、阈值、并发数等参数支持运行期热更新, 下一轮生效.
//
// 该包属于内部实现, 不应被业务代码直接导入.
package engine

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/474420502/execute/basic"
)

// Mode 决定执行时机.
type Mode int

const (
	// ModeInterval 固定节奏: 每隔 Interval 把缓冲区中的数据作为一个批次执行.
	ModeInterval Mode = iota
	// ModeCompensate 固定速率补偿: 批次执行耗时超过 Interval 时, 下一轮不额外等待,
	// 使长期平均速率稳定在 1/Interval.
	ModeCompensate
	// ModeConcurrent 并发批量: 每隔 Interval 取走缓冲区中的数据, 交给最多 Concurrency
	// 个 worker 并发执行.
	ModeConcurrent
	// ModeThreshold 阈值触发: 数据条数达到 BatchSize 立即执行; Interval 作为兜底,
	// 定期把不足阈值的数据也执行掉.
	ModeThreshold
	// ModeTrigger 被动触发: 不使用定时器, 每次数据到达时把当前缓冲区中的数据
	// 合并成一个批次立即执行.
	ModeTrigger
)

func (m Mode) String() string {
	switch m {
	case ModeInterval:
		return "interval"
	case ModeCompensate:
		return "compensate"
	case ModeConcurrent:
		return "concurrent"
	case ModeThreshold:
		return "threshold"
	case ModeTrigger:
		return "trigger"
	default:
		return "unknown"
	}
}

// Backpressure 决定队列/并发槽位不足时的行为.
type Backpressure int

const (
	// BackpressureBlock 阻塞等待(背压), 不丢数据. 默认值.
	BackpressureBlock Backpressure = iota
	// BackpressureDrop 丢弃当前数据并计数, 不阻塞调用方.
	BackpressureDrop
)

func (b Backpressure) String() string {
	if b == BackpressureDrop {
		return "drop"
	}
	return "block"
}

const (
	defaultQueueSize   = 1 << 16
	defaultBufferCap   = 1024
	defaultRecoverKeep = 8
)

// Config 是执行引擎的完整配置.
//
// 创建期固定: Handler、PeriodicHandler、SizeHandler、Mode、QueueSize、BufferCap。
// 运行期可热更新(见 Options/Configure): Interval、BatchSize、Concurrency、
// Backpressure、MaxPending、Recover、OnExecute、OnDrop、Logf。
type Config[ITEM any] struct {
	// Handler 是默认处理器.
	Handler basic.Handler[ITEM]
	// PeriodicHandler 在"周期兜底"路径覆盖 Handler(可选).
	PeriodicHandler basic.Handler[ITEM]
	// SizeHandler 在"数量阈值"路径覆盖 Handler(可选).
	SizeHandler basic.Handler[ITEM]

	// Mode 是执行时机.
	Mode Mode
	// Interval 是执行周期.
	Interval time.Duration
	// BatchSize 是数量阈值, 仅 ModeThreshold 使用; <=0 表示禁用数量触发.
	BatchSize int
	// Concurrency 是并发执行批次的 worker 数量; <=0 时取 runtime.NumCPU().
	Concurrency int
	// QueueSize 是采集队列容量; <=0 时取默认值 65536.
	QueueSize int
	// BufferCap 是批次缓冲区初始容量; <=0 时取 BatchSize 或 1024.
	BufferCap int

	// Backpressure 决定背压策略.
	Backpressure Backpressure
	// MaxPending 限制缓冲区积压上限; <=0 表示不限制. 超出即丢弃并计数.
	MaxPending int
	// DeferOnThreshold 仅 ModeConcurrent 有效: 为 true 时达到数量阈值立即派发,
	// 而不是等下一个节拍(threshold 模式内部会打开它).
	DeferOnThreshold bool

	// Recover 在 Handler panic 时被调用(可选).
	Recover basic.RecoverFunc
	// OnExecute 每次批次执行完成后同步回调(必须轻量).
	OnExecute func(basic.ExecuteEvent)
	// OnDrop 每次丢弃时同步回调(必须轻量).
	OnDrop func(basic.DropEvent)
	// Logf 是内部诊断日志(默认静默).
	Logf func(format string, args ...any)

	// RecoverKeep 是保留的最近失败记录条数; <=0 时取默认值 8.
	RecoverKeep int
	// Name 用于日志与指标标识.
	Name string
}

// Options 是运行期可热更新的配置子集.
//
// 与 Config 分离的原因: WithInterval(0) 这类调用必须能区分
// "未设置" 与 "显式设置为零值"(后者用于停用某个触发条件).
type Options[ITEM any] struct {
	// Interval / SetInterval 表示是否显式设置了周期; 未设置时保留原值.
	Interval    time.Duration
	SetInterval bool

	BatchSize int
	SetSize   bool

	Backpressure    Backpressure
	SetBackpressure bool

	Concurrency    int
	SetConcurrency bool

	MaxPending    int
	SetMaxPending bool

	Recover   basic.RecoverFunc
	OnExecute func(basic.ExecuteEvent)
	OnDrop    func(basic.DropEvent)
	Logf      func(format string, args ...any)

	// DeferOnThreshold 是创建期语义: 仅当 SetDeferOnThreshold 为 true 时生效.
	DeferOnThreshold    bool
	SetDeferOnThreshold bool

	// PeriodicHandler / SizeHandler 为空表示保留原处理器.
	PeriodicHandler basic.Handler[ITEM]
	SizeHandler     basic.Handler[ITEM]
}

// Option 是对运行期配置的修改, 可安全地在运行期重复应用.
type Option[ITEM any] func(*Options[ITEM])

// WithInterval 设置执行周期(d > 0), 运行期热更新在下一个网格点生效.
func WithInterval[ITEM any](d time.Duration) Option[ITEM] {
	return func(o *Options[ITEM]) {
		if d <= 0 {
			return // 拒绝非法值, 避免执行器被静默关停
		}
		o.Interval = d
		o.SetInterval = true
	}
}

// WithBatchSize 设置数量阈值(size > 0).
func WithBatchSize[ITEM any](size int) Option[ITEM] {
	return func(o *Options[ITEM]) {
		if size <= 0 {
			return
		}
		o.BatchSize = size
		o.SetSize = true
	}
}

// WithConcurrency 设置并发 worker 数量.
func WithConcurrency[ITEM any](n int) Option[ITEM] {
	return func(o *Options[ITEM]) {
		if n < 1 {
			n = 1
		}
		o.Concurrency = n
		o.SetConcurrency = true
	}
}

// WithBackpressure 设置背压策略.
func WithBackpressure[ITEM any](bp Backpressure) Option[ITEM] {
	return func(o *Options[ITEM]) {
		o.Backpressure = bp
		o.SetBackpressure = true
	}
}

// WithMaxPending 设置缓冲区积压上限; n <= 0 表示不限制.
func WithMaxPending[ITEM any](n int) Option[ITEM] {
	return func(o *Options[ITEM]) {
		o.MaxPending = n
		o.SetMaxPending = true
	}
}

// WithPeriodicHandler 设置周期兜底处理器.
func WithPeriodicHandler[ITEM any](h basic.Handler[ITEM]) Option[ITEM] {
	return func(o *Options[ITEM]) { o.PeriodicHandler = h }
}

// WithSizeHandler 设置数量阈值处理器.
func WithSizeHandler[ITEM any](h basic.Handler[ITEM]) Option[ITEM] {
	return func(o *Options[ITEM]) { o.SizeHandler = h }
}

// WithDeferOnThreshold 仅在 ModeConcurrent 下有意义: 为 true 时达到数量阈值
// 立即派发批次, 而不是等下一个节拍. 使用它必须同时提供 BatchSize.
func WithDeferOnThreshold[ITEM any](v bool) Option[ITEM] {
	return func(o *Options[ITEM]) {
		o.DeferOnThreshold = v
		o.SetDeferOnThreshold = true
	}
}

// WithRecover 设置 panic 恢复回调.
func WithRecover[ITEM any](r basic.RecoverFunc) Option[ITEM] {
	return func(o *Options[ITEM]) { o.Recover = r }
}

// WithOnExecute 设置批次执行完成回调.
func WithOnExecute[ITEM any](fn func(basic.ExecuteEvent)) Option[ITEM] {
	return func(o *Options[ITEM]) { o.OnExecute = fn }
}

// WithOnDrop 设置丢弃回调.
func WithOnDrop[ITEM any](fn func(basic.DropEvent)) Option[ITEM] {
	return func(o *Options[ITEM]) { o.OnDrop = fn }
}

// WithLogf 设置内部诊断日志.
func WithLogf[ITEM any](fn func(format string, args ...any)) Option[ITEM] {
	return func(o *Options[ITEM]) { o.Logf = fn }
}

// Trigger 说明一次批次执行的来源, 决定 threshold 模式下使用哪个处理器.
type Trigger uint8

const (
	// TriggerPeriodic 周期性兜底(节拍到点)或显式 Flush.
	TriggerPeriodic Trigger = iota
	// TriggerThreshold 数据量达到数量阈值.
	TriggerThreshold
	// TriggerArrival 数据到达即执行(ModeTrigger).
	TriggerArrival
)

func (t Trigger) String() string {
	switch t {
	case TriggerPeriodic:
		return "periodic"
	case TriggerThreshold:
		return "threshold"
	case TriggerArrival:
		return "arrival"
	default:
		return "unknown"
	}
}

type cmdKind uint8

const (
	cmdValue cmdKind = iota
	cmdFlush
	cmdPhase
)

type cmd[ITEM any] struct {
	kind  cmdKind
	value ITEM
	ack   chan error
	// phase 是 cmdPhase 的目标状态(int32(basic.Status)).
	phase int32
	// release 在 dispatcher 处理完该命令后关闭, 作为"命令已生效"的同步点.
	release chan struct{}
}

type runConfig[ITEM any] struct {
	interval         time.Duration
	batchSize        int
	concurrency      int
	backpressure     Backpressure
	maxPending       int
	deferOnThreshold bool
	recover          basic.RecoverFunc
	onExecute        func(basic.ExecuteEvent)
	onDrop           func(basic.DropEvent)
	logf             func(format string, args ...any)
	periodicDo       basic.Handler[ITEM]
	sizeDo           basic.Handler[ITEM]
}

// Engine 是执行引擎.
//
// 除生命周期状态与指标外, 所有业务可变状态都由 dispatcher 独占:
// buffer 只被 dispatcher 与 Flush/Close 的"取走"路径触碰, 由 owners 保护.
type Engine[ITEM any] struct {
	name        string
	mode        Mode
	handler     basic.Handler[ITEM]
	queueSize   int
	bufferCap   int
	recoverKeep int

	cfgmu sync.RWMutex
	cfg   runConfig[ITEM]

	cmds       chan cmd[ITEM]
	phase      atomic.Int32
	closed     atomic.Bool
	stopSignal chan struct{}
	// stopCtx 在 stopSignal 关闭时被取消, 用于中断信号量等待.
	stopCtx  context.Context
	stopFn   context.CancelFunc
	loopDone chan struct{}

	startOnce    sync.Once
	completeOnce sync.Once

	// tick 提供可重配置的周期节拍; Trigger 模式下为 nil.
	tick *delayQueue
	// lastTick 是 dispatcher 最近一次处理的节拍时刻(仅 dispatcher 读写).
	lastTick time.Time
	// lastExec 是补偿模式下最近一次批次执行的开始时间, 决定下一个截止时刻.
	// 仅 dispatcher 读写.
	lastExec time.Time

	// slot 是并发信号量, 容量即并发上限; 仅 dispatcher 获取, worker 归还.
	slot *basic.Semaphore

	owners sync.Mutex
	buffer []ITEM

	inflight sync.WaitGroup

	statmu      sync.Mutex
	received    atomic.Uint64
	processed   atomic.Uint64
	succeeded   atomic.Uint64
	failedItems atomic.Uint64
	dropped     atomic.Uint64
	executions  atomic.Uint64
	failedExecs atomic.Uint64
	inFlight    atomic.Int64
	lastErr     string
	lastErrAt   time.Time
	recent      []basic.ErrorRecord
}

// New 创建执行引擎, 并在首次采集时自动启动执行循环.
func New[ITEM any](cfg Config[ITEM], opts ...Option[ITEM]) (*Engine[ITEM], error) {
	if cfg.Handler == nil && cfg.PeriodicHandler == nil && cfg.SizeHandler == nil {
		return nil, errors.New("execute: handler must not be nil")
	}
	if cfg.Mode < ModeInterval || cfg.Mode > ModeTrigger {
		return nil, fmt.Errorf("execute: unknown mode %d", cfg.Mode)
	}

	qsize := cfg.QueueSize
	if qsize <= 0 {
		qsize = defaultQueueSize
	}
	bcap := cfg.BufferCap
	if bcap <= 0 {
		bcap = cfg.BatchSize
	}
	if bcap <= 0 {
		bcap = defaultBufferCap
	}
	keep := cfg.RecoverKeep
	if keep <= 0 {
		keep = defaultRecoverKeep
	}

	conc := cfg.Concurrency
	if conc <= 0 {
		conc = runtime.NumCPU()
		if conc < 1 {
			conc = 1
		}
	}

	e := &Engine[ITEM]{
		name:        cfg.Name,
		mode:        cfg.Mode,
		handler:     cfg.Handler,
		queueSize:   qsize,
		bufferCap:   bcap,
		recoverKeep: keep,
		cmds:        make(chan cmd[ITEM], qsize),
		stopSignal:  make(chan struct{}),
		loopDone:    make(chan struct{}),
		buffer:      make([]ITEM, 0, bcap),
		slot:        basic.NewSemaphore(conc),
	}
	e.stopCtx, e.stopFn = context.WithCancel(context.Background())
	e.cfg = runConfig[ITEM]{
		interval:         cfg.Interval,
		batchSize:        cfg.BatchSize,
		concurrency:      conc,
		backpressure:     cfg.Backpressure,
		maxPending:       cfg.MaxPending,
		deferOnThreshold: cfg.DeferOnThreshold,
		recover:          cfg.Recover,
		onExecute:        cfg.OnExecute,
		onDrop:           cfg.OnDrop,
		logf:             cfg.Logf,
		periodicDo:       cfg.PeriodicHandler,
		sizeDo:           cfg.SizeHandler,
	}
	e.phase.Store(int32(basic.StatusCreated))

	// 先应用创建期选项, 再启动定时节拍, 最后再暴露给调用方.
	// 顺序很重要: 若在 ApplyOptions 之前就初始化节拍, 运行期配置的周期会被覆盖;
	// 而提前启动 dispatcher 则会让调用方在构造返回前就观察到半初始化状态.
	e.applyOptions(opts...)
	e.resizeSlots(e.currentConcurrency()) // 选项可能覆盖了 Concurrency, 必须同步信号量容量
	e.initTicker()
	return e, nil
}

// applyOptions 应用运行期配置(同 Configure), 但不会触碰需要节拍器的收尾逻辑.
func (e *Engine[ITEM]) applyOptions(opts ...Option[ITEM]) {
	if len(opts) == 0 {
		return
	}
	var o Options[ITEM]
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	e.cfgmu.Lock()
	if o.SetInterval {
		e.cfg.interval = o.Interval
	}
	if o.SetSize {
		e.cfg.batchSize = o.BatchSize
	}
	if o.SetConcurrency {
		e.cfg.concurrency = o.Concurrency
	}
	if o.SetBackpressure {
		e.cfg.backpressure = o.Backpressure
	}
	if o.SetMaxPending {
		e.cfg.maxPending = o.MaxPending
	}
	if o.Recover != nil {
		e.cfg.recover = o.Recover
	}
	if o.OnExecute != nil {
		e.cfg.onExecute = o.OnExecute
	}
	if o.OnDrop != nil {
		e.cfg.onDrop = o.OnDrop
	}
	if o.Logf != nil {
		e.cfg.logf = o.Logf
	}
	if o.PeriodicHandler != nil {
		e.cfg.periodicDo = o.PeriodicHandler
	}
	if o.SizeHandler != nil {
		e.cfg.sizeDo = o.SizeHandler
	}
	if o.SetDeferOnThreshold {
		// 只允许"由关到开": 该标志是创建期语义, 运行期不应被无意关闭.
		e.cfg.deferOnThreshold = o.DeferOnThreshold
	}
	e.cfgmu.Unlock()
}

// Configure 应用运行期配置(热更新). 传入的 Option 立即生效:
// 周期在下一个网格点生效, 阈值/并发数等在下一轮生效, 不丢数据、不需要重启.
func (e *Engine[ITEM]) Configure(opts ...Option[ITEM]) {
	if len(opts) == 0 {
		return
	}
	e.applyOptions(opts...)

	e.cfgmu.RLock()
	concurrency := e.cfg.concurrency
	interval := e.cfg.interval
	e.cfgmu.RUnlock()

	if concurrency < 1 {
		concurrency = 1
	}
	e.resizeSlots(concurrency)
	e.syncTicker(interval)
}

// initTicker 依据模式创建/配置节拍器.
func (e *Engine[ITEM]) initTicker() {
	if e.mode == ModeTrigger {
		return
	}
	mode := tickSkip
	if e.mode == ModeCompensate {
		mode = tickCompensate
	}
	e.tick = newDelayQueue(mode)
	e.syncTicker(e.currentInterval())
}

// syncTicker 让节拍器与当前周期保持一致.
func (e *Engine[ITEM]) syncTicker(interval time.Duration) {
	if e.tick == nil {
		return
	}
	// 热更新的语义是"立即按新周期重新计时": 若沿用旧网格, 缩小周期后可能要等到
	// 旧网格点才首次触发, 会让调用方误以为没有生效.
	e.tick.Set(interval)
}

// resizeSlots 让信号量容量与目标并发上限一致.
//
// Semaphore.Resize 会保持"在途批次持有的 permit 数"不变, 因此热更新并发上限
// 不会误放行或误阻塞正在执行的批次; 缩容时只回收空闲槽位。
func (e *Engine[ITEM]) resizeSlots(want int) {
	if want < 1 {
		want = 1
	}
	if got := e.slot.Resize(want); got != want {
		e.logf("execute: 并发上限暂未能调整到 %d(当前 %d): 在途批次多于新上限, 归还后会自动收敛", want, got)
	}
}

// Start 启动 dispatcher; 重复调用无副作用.
func (e *Engine[ITEM]) Start() {
	e.startOnce.Do(func() {
		e.phase.Store(int32(basic.StatusRunning))
		e.inflight.Add(1) // 让 Wait 覆盖 dispatcher 自身
		go e.run()
	})
}

// Collector 返回一个独立的采集句柄.
//
// 采集与执行解耦的关键: 句柄可在多个 goroutine / 子系统之间传递, 各自持有一份
// 引用互不干扰; 引擎关闭后句柄立即失效(返回 ErrClosed), 无需额外的同步协议.
func (e *Engine[ITEM]) Collector() *Collector[ITEM] {
	return &Collector[ITEM]{engine: e}
}

// Collect 收集一条数据(默认句柄); 队列满时按背压策略阻塞, 关闭后返回 ErrClosed.
func (e *Engine[ITEM]) Collect(item ITEM) error {
	e.Start()
	return e.collect(item, false)
}

// TryCollect 非阻塞收集; 队列满时返回 ErrQueueFull 并计数.
func (e *Engine[ITEM]) TryCollect(item ITEM) error {
	e.Start()
	return e.collect(item, true)
}

func (e *Engine[ITEM]) collect(item ITEM, nonblock bool) error {
	if e.closed.Load() {
		return basic.ErrClosed
	}
	if nonblock {
		select {
		case e.cmds <- cmd[ITEM]{kind: cmdValue, value: item}:
			return nil
		default:
			e.countDrop("queue-full", 0)
			return basic.ErrQueueFull
		}
	}
	// 快路径: 队列未满时直接入队, 避免 select 在关闭瞬间的随机性.
	select {
	case e.cmds <- cmd[ITEM]{kind: cmdValue, value: item}:
		return nil
	default:
	}
	// 慢路径: 队列已满, 进入背压等待, 同时响应关闭信号.
	select {
	case e.cmds <- cmd[ITEM]{kind: cmdValue, value: item}:
		return nil
	case <-e.stopSignal:
		return basic.ErrClosed
	}
}

// Flush 让当前缓冲区中的数据立即执行, 并等待执行完成.
//
// 常驻循环的执行器一般不需要它; 主要用于 ModeTrigger 的多生产者场景
// (把一批数据手动刷出去)以及测试.
func (e *Engine[ITEM]) Flush() error {
	return e.flush(false)
}

// FlushAndDrain 语义同 Flush, 但在执行器已关闭时改为同步执行残留数据(尽力而为).
func (e *Engine[ITEM]) FlushAndDrain() error { return e.flush(true) }

func (e *Engine[ITEM]) flush(drain bool) error {
	if e.Status() == basic.StatusStopped {
		if !drain {
			return basic.ErrClosed
		}
		return e.flushOwned(TriggerPeriodic, true)
	}
	e.Start()

	ack := make(chan error, 1)
	select {
	case e.cmds <- cmd[ITEM]{kind: cmdFlush, ack: ack}:
	case <-e.stopSignal:
		if !drain {
			return basic.ErrClosed
		}
		return e.flushOwned(TriggerPeriodic, true)
	}

	select {
	case err := <-ack:
		return err
	case <-e.loopDone:
		if !drain {
			return basic.ErrClosed
		}
		return e.flushOwned(TriggerPeriodic, true)
	}
}

// Pause 暂停执行: 仍可收集数据, 但不会触发处理.
//
// 该调用会等待状态真正切换完成(dispatcher 处理到该命令)后返回, 因此
// "Pause 返回后新采集的数据一定不会被自动执行".
// 被暂停前已经入队的数据仍会保留在缓冲区中, Resume 后继续处理.
func (e *Engine[ITEM]) Pause() {
	e.setPhase(basic.StatusPaused)
}

// Resume 从暂停中恢复; 返回后周期性执行重新生效.
func (e *Engine[ITEM]) Resume() {
	e.setPhase(basic.StatusRunning)
}

// Await 阻塞直到 dispatcher 处理完此前提交的所有数据, 并返回其后的指标快照.
//
// 典型用法: 批量投递数据后调用 Await, 确保指标(如 DroppedItems)已经结算.
// 注意: 它只保证"命令已处理完成", 若节拍触发的批次正在并发执行, 可能仍有
// InFlight 中的批次未结束, 此时请配合 Wait 使用.
func (e *Engine[ITEM]) Await() basic.Stats {
	e.Start()
	release := make(chan struct{})
	select {
	case e.cmds <- cmd[ITEM]{kind: cmdPhase, release: release}: // 空操作同步点
	case <-e.stopSignal:
		return e.Stats()
	}
	select {
	case <-release:
	case <-e.loopDone:
	}
	return e.Stats()
}

func (e *Engine[ITEM]) setPhase(target basic.Status) {
	e.Start()
	release := make(chan struct{})
	select {
	case e.cmds <- cmd[ITEM]{kind: cmdPhase, phase: int32(target), release: release}:
	case <-e.stopSignal:
		return
	}
	select {
	case <-release:
	case <-e.loopDone:
	}
}

// Status 返回生命周期状态.
func (e *Engine[ITEM]) Status() basic.Status { return basic.Status(e.phase.Load()) }

// Done 返回在 dispatcher 完全退出后关闭的通道.
func (e *Engine[ITEM]) Done() <-chan struct{} { return e.loopDone }

// Close 优雅关闭: 幂等, 不阻塞等待执行结束; 残留数据会在退出前尽力执行一次.
func (e *Engine[ITEM]) Close() {
	if !e.closed.CompareAndSwap(false, true) {
		return
	}

	started := true
	e.startOnce.Do(func() {
		// 从未启动: 无 goroutine 可停, 直接进入终态.
		started = false
		e.phase.Store(int32(basic.StatusStopped))
		e.completeOnce.Do(func() { close(e.loopDone) })
	})

	e.stopFn()
	close(e.stopSignal)
	if started {
		e.phase.Store(int32(basic.StatusClosing))
	}
}

// Wait 阻塞等待执行器完全停止, ctx 用于超时控制.
func (e *Engine[ITEM]) Wait(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.Close()
	select {
	case <-e.loopDone:
		e.inflight.Wait()
		return nil
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.loopDone:
		e.inflight.Wait()
		return nil
	}
}

// Stats 返回运行指标快照.
func (e *Engine[ITEM]) Stats() basic.Stats {
	e.owners.Lock()
	pending := len(e.buffer)
	e.owners.Unlock()

	e.statmu.Lock()
	s := basic.Stats{
		ReceivedItems:    e.received.Load(),
		ProcessedItems:   e.processed.Load(),
		SucceededItems:   e.succeeded.Load(),
		FailedItems:      e.failedItems.Load(),
		DroppedItems:     e.dropped.Load(),
		Executions:       e.executions.Load(),
		FailedExecutions: e.failedExecs.Load(),
		PendingItems:     pending,
		InFlight:         e.inFlight.Load(),
		LastError:        e.lastErr,
		LastErrorAt:      e.lastErrAt,
	}
	if n := len(e.recent); n > 0 {
		s.RecentErrors = make([]basic.ErrorRecord, n)
		copy(s.RecentErrors, e.recent)
	}
	e.statmu.Unlock()
	return s
}

// Pending 返回缓冲区中待处理的数据条数.
func (e *Engine[ITEM]) Pending() int {
	e.owners.Lock()
	defer e.owners.Unlock()
	return len(e.buffer)
}

// ---------------------------------------------------------------------------
// dispatcher
// ---------------------------------------------------------------------------

func (e *Engine[ITEM]) run() {
	defer e.inflight.Done()
	defer func() {
		// 顺序: 先关 loopDone 让所有等待者返回, 再落终态.
		e.completeOnce.Do(func() { close(e.loopDone) })
		e.phase.Store(int32(basic.StatusStopped))
	}()

	var tick <-chan struct{}
	if e.tick != nil {
		defer e.tick.Stop()
		tick = e.tick.C()
	}

	for {
		select {
		case c := <-e.cmds:
			e.dispatch(c)
		case <-tick:
			// 使用"信号被处理的时刻"而不是"信号被投递的时刻": 信号可能在通道里
			// 排队, 若采用投递时刻, 补偿模式会误判截止时间而多等一个周期.
			e.lastTick = time.Now()
			e.onTick()
		case <-e.stopSignal:
			e.shutdown()
			return
		}
	}
}

func (e *Engine[ITEM]) dispatch(c cmd[ITEM]) {
	switch c.kind {
	case cmdValue:
		e.doCollect(c.value)
	case cmdFlush:
		err := e.flushOwned(TriggerPeriodic, true)
		if c.ack != nil {
			c.ack <- err
		}
	case cmdPhase:
		if c.phase != 0 {
			e.applyPhase(basic.Status(c.phase))
		}
		if c.release != nil {
			close(c.release)
		}
	}
}

func (e *Engine[ITEM]) onTick() {
	if e.Status() == basic.StatusPaused {
		return // 暂停: 保留数据, 不触发执行.
	}
	switch e.mode {
	case ModeTrigger:
		// 被动触发模式没有节拍.
	case ModeThreshold:
		// 兜底: 把不足阈值的数据也执行掉, 使用周期处理器.
		e.flushOwned(TriggerPeriodic, true)
	case ModeCompensate:
		// 固定速率补偿: 心跳只负责"检查截止时刻", 判定逻辑见 tryCompensate.
		e.tryCompensate()
	default:
		e.flushOwned(TriggerPeriodic, true)
	}
}

// tryCompensate 是固定速率补偿的核心判定(只在 dispatcher goroutine 中调用).
//
// 模型: 每次实际执行都会把"下一个截止时刻"推进为 lastExec + interval。于是:
//
//   - 批次耗时超过周期 -> 截止时刻已过期 -> 心跳一到就立刻执行下一批,
//     不会在批次之间额外插入一个周期的等待;
//   - 批次很快         -> 数据到达时截止时刻尚未到 -> 等到该时刻再执行(慢启动),
//     从而把长期平均速率稳定在 1/interval, 同时不超过上游速率。
//
// 返回是否真正执行了一个批次。
func (e *Engine[ITEM]) tryCompensate() bool {
	interval := e.currentInterval()
	if interval <= 0 {
		return false
	}
	if e.Pending() == 0 {
		// 没有数据: 不推进截止时刻, 数据一到就能立刻开始.
		return false
	}
	if e.lastExec.IsZero() {
		e.lastExec = time.Now().Add(-interval) // 首次不做慢启动
	} else if now := time.Now(); now.Before(e.lastExec.Add(interval)) {
		return false // 截止时刻未到
	}

	execAt := time.Now()
	e.flushOwned(TriggerPeriodic, true)
	e.lastExec = execAt

	if e.Pending() > 0 {
		// 仍有积压(水位高于消费能力): 下一个心跳继续抽.
		e.lastExec = time.Time{}
	}
	return true
}

// applyPhase 应用状态切换(只在 dispatcher goroutine 中调用).
func (e *Engine[ITEM]) applyPhase(target basic.Status) {
	switch target {
	case basic.StatusPaused:
		if e.phase.Load() == int32(basic.StatusRunning) || e.phase.Load() == int32(basic.StatusCreated) {
			e.phase.Store(int32(basic.StatusPaused))
		}
	case basic.StatusRunning:
		if e.phase.Load() == int32(basic.StatusPaused) {
			e.phase.Store(int32(basic.StatusRunning))
		}
	}
}

// shutdown 是 dispatcher 退出前的收尾: 尽力执行残留数据.
func (e *Engine[ITEM]) shutdown() {
	trigger := TriggerPeriodic
	if e.mode == ModeThreshold {
		trigger = TriggerPeriodic // 收尾一律走周期/兜底处理器
	}
	e.flushOwned(trigger, true)
	e.flushOwned(trigger, true) // 执行期间可能又落进少量数据(比如被唤醒的采集者)
	e.drainBuffer()
}

// ---------------------------------------------------------------------------
// 采集
// ---------------------------------------------------------------------------

func (e *Engine[ITEM]) doCollect(item ITEM) {
	if e.Status() == basic.StatusPaused {
		// 暂停: 数据仍然缓存, 只是不触发执行.
		e.received.Add(1)
		e.pushBuffer(item)
		return
	}

	e.received.Add(1)
	if !e.pushBuffer(item) {
		return
	}
	switch e.mode {
	case ModeTrigger:
		e.flushOwned(TriggerArrival, true)
		return
	case ModeCompensate:
		// 数据到达时若截止时刻已过则立即开始, 避免固定速率被节拍粒度拖慢.
		if e.tryCompensate() {
			return
		}
	}
	if e.shouldFlushOnSize() {
		e.flushOwned(TriggerThreshold, true)
	}
}

// pushBuffer 把数据追加进批次缓冲区; 返回 false 表示被丢弃.
func (e *Engine[ITEM]) pushBuffer(item ITEM) bool {
	e.owners.Lock()
	if limit := e.currentMaxPending(); limit > 0 && len(e.buffer) >= limit {
		pending := len(e.buffer)
		e.owners.Unlock()
		e.countDrop("max-pending", pending)
		return false
	}
	e.buffer = append(e.buffer, item)
	e.owners.Unlock()
	return true
}

// shouldFlushOnSize 判断是否触发数量阈值.
//
// ModeConcurrent 默认不在阈值处立即派发, 而是等下一个节拍统一切分批次,
// 避免到达速率波动导致并发 worker 数量失控; threshold 模式会打开 deferOnThreshold
// 的对立面(即立即派发), 保持"数量阈值即时执行"的语义.
func (e *Engine[ITEM]) shouldFlushOnSize() bool {
	e.cfgmu.RLock()
	size, direct := e.cfg.batchSize, e.cfg.deferOnThreshold
	e.cfgmu.RUnlock()

	if size <= 0 {
		return false
	}
	if e.mode == ModeConcurrent && !direct {
		return false
	}
	return e.Pending() >= size
}

// ---------------------------------------------------------------------------
// 执行
// ---------------------------------------------------------------------------

// flushOwned 把缓冲区数据交给 Handler; 并发模式下受信号量约束.
func (e *Engine[ITEM]) flushOwned(trigger Trigger, allowWait bool) error {
	items := e.takeBuffer()
	if len(items) == 0 {
		return nil
	}

	handler, blocking := e.route(trigger)
	if handler == nil {
		e.succeed(items)
		return nil
	}

	if e.mode != ModeConcurrent {
		e.runHandler(handler, items)
		return nil
	}
	return e.submit(handler, items, blocking, allowWait)
}

// route 依据批次来源选择处理器.
//
// threshold 模式下: 达到数量阈值的批次使用 SizeHandler, 周期兜底与显式 Flush
// 使用 PeriodicHandler; 未配置对应回调时回落到默认 Handler.
// 其余模式一律使用默认 Handler.
func (e *Engine[ITEM]) route(trigger Trigger) (handler basic.Handler[ITEM], blocking bool) {
	e.cfgmu.RLock()
	defer e.cfgmu.RUnlock()

	blocking = e.cfg.backpressure == BackpressureBlock
	if e.mode != ModeThreshold {
		return e.handler, blocking
	}

	h := e.cfg.periodicDo
	if trigger == TriggerThreshold && e.cfg.sizeDo != nil {
		h = e.cfg.sizeDo
	}
	if h == nil {
		h = e.handler
	}
	if h == nil {
		// 极端情况: 调用方只配置了另一个模式的处理器, 退化为它能用的任意一个.
		if trigger == TriggerThreshold {
			h = e.cfg.periodicDo
		} else {
			h = e.cfg.sizeDo
		}
	}
	return h, blocking
}

// takeBuffer 取走当前缓冲区内容的所有权.
//
// 非并发模式下切片会被引擎复用, 因此 Handler 不能跨调用持有 items;
// 并发模式下批次异步执行, 这里返回独立切片以保证安全.
func (e *Engine[ITEM]) takeBuffer() []ITEM {
	e.owners.Lock()
	defer e.owners.Unlock()

	if len(e.buffer) == 0 {
		return nil
	}
	if e.mode != ModeConcurrent {
		items := e.buffer
		e.buffer = e.buffer[:0]
		return items
	}
	items := make([]ITEM, len(e.buffer))
	copy(items, e.buffer)
	e.buffer = e.buffer[:0]
	return items
}

// drainBuffer 丢弃缓冲区残留数据(退出路径).
func (e *Engine[ITEM]) drainBuffer() {
	e.owners.Lock()
	pending := len(e.buffer)
	if pending > 0 {
		var zero ITEM
		for i := range e.buffer {
			e.buffer[i] = zero // 断开引用, 便于 GC
		}
		e.buffer = e.buffer[:0]
	}
	e.owners.Unlock()
	if pending > 0 {
		e.dropped.Add(uint64(pending))
		e.emitDrop("drained", pending)
	}
}

// submit 派发一个并发批次.
func (e *Engine[ITEM]) submit(handler basic.Handler[ITEM], items []ITEM, blocking, allowWait bool) error {
	switch {
	case blocking && allowWait:
		// 背压: 等待一个空闲 worker; 关闭时不再等待, 直接同步执行避免静默丢数据.
		acquired, err := e.slot.Acquire(e.stopCtx)
		if err != nil {
			// 关闭过程中不再等待 worker: 尊重并发上限, 丢弃本批次并计数,
			// 而不是让 dispatcher 亲自同步执行(那会突破并发上限).
			e.dropBatch("closing", items)
			return basic.ErrClosed
		}
		if !acquired {
			return nil
		}
	default:
		// 丢弃策略: 没有空闲 worker 就丢弃本批次.
		if !e.slot.TryAcquire() {
			e.dropBatch("workers-busy", items)
			return basic.ErrQueueFull
		}
	}

	e.inflight.Add(1)
	e.inFlight.Add(1)
	go func() {
		defer func() {
			e.inFlight.Add(-1)
			e.inflight.Done()
			e.slot.Release()
		}()
		e.runHandler(handler, items)
	}()
	return nil
}

// runHandler 是唯一真正调用用户回调的地方, 负责 panic 隔离、指标与观测.
func (e *Engine[ITEM]) runHandler(handler basic.Handler[ITEM], items []ITEM) {
	start := time.Now()
	err, panicked := e.safeExec(handler, items)
	elapsed := time.Since(start)

	e.executions.Add(1)
	e.processed.Add(uint64(len(items)))
	if err == nil {
		e.succeeded.Add(uint64(len(items)))
	} else {
		e.failedExecs.Add(1)
		e.failedItems.Add(uint64(len(items)))
		e.recordError(basic.ErrorRecord{
			At:       start,
			Items:    len(items),
			Err:      err.Error(),
			Panicked: panicked,
		})
	}

	e.cfgmu.RLock()
	onExecute := e.cfg.onExecute
	e.cfgmu.RUnlock()
	if onExecute != nil {
		onExecute(basic.ExecuteEvent{
			Items:    len(items),
			Err:      err,
			Panicked: panicked,
			Duration: elapsed,
			Time:     start,
		})
	}
}

func (e *Engine[ITEM]) succeed(items []ITEM) {
	e.executions.Add(1)
	e.processed.Add(uint64(len(items)))
	e.succeeded.Add(uint64(len(items)))
}

// safeExec 隔离用户回调的 panic, 并按需调用 Recover 回调.
func (e *Engine[ITEM]) safeExec(handler basic.Handler[ITEM], items []ITEM) (err error, panicked bool) {
	e.cfgmu.RLock()
	recoverFn := e.cfg.recover
	name := e.name
	e.cfgmu.RUnlock()

	defer func() {
		ierr := recover()
		if ierr == nil {
			return
		}
		panicked = true
		if recoverFn != nil {
			func() { // Recover 回调自身再 panic 也不能让 dispatcher 倒下.
				defer func() { _ = recover() }()
				recoverFn(ierr)
			}()
		}
		err = fmt.Errorf("execute: panic recovered: %v", ierr)
		if name != "" {
			err = fmt.Errorf("execute[%s]: %w", name, err)
		}
		if e.logEnabled() {
			e.logf("%v\n%s", err, debug.Stack())
		}
	}()

	return handler(items), false
}

func (e *Engine[ITEM]) logEnabled() bool {
	e.cfgmu.RLock()
	defer e.cfgmu.RUnlock()
	return e.cfg.logf != nil
}

func (e *Engine[ITEM]) recordError(rec basic.ErrorRecord) {
	e.statmu.Lock()
	e.lastErr = rec.Err
	e.lastErrAt = rec.At
	e.recent = append(e.recent, rec)
	if len(e.recent) > e.recoverKeep {
		e.recent = append(e.recent[:0], e.recent[len(e.recent)-e.recoverKeep:]...)
	}
	e.statmu.Unlock()
}

func (e *Engine[ITEM]) countDrop(reason string, pending int) {
	e.dropped.Add(1)
	e.emitDrop(reason, pending)
}

func (e *Engine[ITEM]) dropBatch(reason string, items []ITEM) {
	if len(items) == 0 {
		return
	}
	e.dropped.Add(uint64(len(items)))
	e.emitDrop(reason, len(items))
}

func (e *Engine[ITEM]) emitDrop(reason string, pending int) {
	e.cfgmu.RLock()
	onDrop := e.cfg.onDrop
	e.cfgmu.RUnlock()
	if onDrop == nil {
		return
	}
	onDrop(basic.DropEvent{At: time.Now(), Reason: reason, PendingItems: pending})
}

// ---------------------------------------------------------------------------
// 配置读取
// ---------------------------------------------------------------------------

func (e *Engine[ITEM]) currentInterval() time.Duration {
	e.cfgmu.RLock()
	defer e.cfgmu.RUnlock()
	return e.cfg.interval
}

func (e *Engine[ITEM]) currentConcurrency() int {
	e.cfgmu.RLock()
	defer e.cfgmu.RUnlock()
	if e.cfg.concurrency < 1 {
		return 1
	}
	return e.cfg.concurrency
}

func (e *Engine[ITEM]) currentMaxPending() int {
	e.cfgmu.RLock()
	defer e.cfgmu.RUnlock()
	return e.cfg.maxPending
}

func (e *Engine[ITEM]) logf(format string, args ...any) {
	e.cfgmu.RLock()
	logf := e.cfg.logf
	e.cfgmu.RUnlock()
	if logf != nil {
		logf(format, args...)
	}
}
