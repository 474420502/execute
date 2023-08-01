package triggered

import (
	"log"
	"sync"
	"sync/atomic"
)

// EventTriggeredExecute实现了一种基于事件触发的执行和错误处理机制
// 可以注册执行函数及恢复函数,通过事件通知触发执行
// 并且具有错误恢复和保护能力

// Event定义了事件类型,Uint32可以支持多达40亿个事件
// type Event uint32

// EventExecute封装了一个执行单元
// 包含执行函数,恢复函数和相关控制参数
type EventExecute[PARAMS any] struct {
	mu sync.Mutex
	// counter用来记录通知次数
	counter int64

	// 达到指定通知次数时触发执行
	notifyOverCountToExecute int64
	executeConcurrentLimit   int64
	executeConcurrent        int64

	// 是否打印恢复日志
	isShowLog atomic.Bool

	shared Shared
	// 要执行的函数
	execDo func(params *Params[PARAMS])

	// 错误恢复函数
	recoverDo func(ierr any)
}

type Shared struct {
	value any // 值传递会复制
	slock sync.Mutex
}

func (s *Shared) SetValue(v any) {
	s.slock.Lock()
	defer s.slock.Unlock()

	s.value = v
}

func (s *Shared) Value(do func(v any)) {
	s.slock.Lock()
	defer s.slock.Unlock()

	do(s.value)
}

type Params[T any] struct {
	Value  *T
	Shared *Shared
}

// EventTriggeredExecute 包含已注册的执行事件列表
// type EventTriggeredExecute[PARAMS any] struct {

// 	// 互斥锁保护events切片
// 	mu sync.Mutex

// 	// 已注册的执行事件列表
// 	events []*EventExecute[PARAMS]
// }

// NewEventTriggeredExecute构造函数
// func NewEventTriggeredExecute[PARAMS any]() *EventTriggeredExecute[PARAMS] {
// 	return &EventTriggeredExecute[PARAMS]{}
// }

func (e *EventExecute[PARAMS]) SetShared(v any) {
	// 加锁

	e.shared.SetValue(v)
}

func (e *EventExecute[PARAMS]) SetConcurrentNum(v int64) {

	e.mu.Lock()
	defer e.mu.Unlock()

	e.executeConcurrentLimit = v
}

// RegisterExecute注册一个执行单元
// 返回分配的事件号
func RegisterExecute[PARAMS any](execDo func(params *Params[PARAMS]), recoverDo func(ierr any)) *EventExecute[PARAMS] {

	// 构造执行单元
	ee := &EventExecute[PARAMS]{
		notifyOverCountToExecute: 1,
		executeConcurrentLimit:   1,
		execDo:                   execDo,
		recoverDo:                recoverDo,
	}

	return ee
}

// Notify用于通知触发执行
// 根据事件号查找执行单元并检查通知次数
// 达到指定次数则触发goroutine异步执行
func (exec *EventExecute[PARAMS]) Notify(params *PARAMS) {

	// 原子加1并检查通知次数

	exec.mu.Lock()
	defer exec.mu.Unlock()

	exec.counter++
	var ok = (exec.counter >= exec.notifyOverCountToExecute) && exec.executeConcurrent < exec.executeConcurrentLimit
	if ok {
		exec.executeConcurrent++
		// 异步goroutine执行
		go func() {

			// recover保护
			defer func() {

				defer func() {
					exec.mu.Lock()
					exec.counter = 0         // reset
					exec.executeConcurrent-- // 执行数量减1
					exec.mu.Unlock()
				}()

				if ierr := recover(); ierr != nil {
					// 打印错误堆栈
					if exec.isShowLog.Load() {
						log.Println(ierr)
					}

					// 调用恢复函数
					if exec.recoverDo != nil {
						exec.recoverDo(ierr)
					}
				}
			}()

			// 执行已注册函数
			exec.execDo(&Params[PARAMS]{
				Shared: &exec.shared,
				Value:  params,
			})
		}()

	}
}
