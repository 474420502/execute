package triggered

import (
	"log"
	"sync"
	"sync/atomic"
)

// EventExecute封装了一个执行单元, 默认是不启动,需要Notify通知触发. 属于被动触发式
// 包含执行函数,恢复函数和相关控制参数
type EventExecute[PARAMS any] struct {
	NULL PARAMS

	mu sync.Mutex
	// counter用来记录通知次数
	counter int64
	// 引用计数
	refCount int64

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
	Value  T
	Shared *Shared
}

func (e *EventExecute[PARAMS]) WithRecover(recoverDo func(ierr any)) *EventExecute[PARAMS] {
	// 加锁
	e.mu.Lock()
	defer e.mu.Unlock()

	e.recoverDo = recoverDo
	return e
}

func (e *EventExecute[PARAMS]) WithShared(v any) *EventExecute[PARAMS] {
	// 加锁

	e.shared.SetValue(v)
	return e
}

func (e *EventExecute[PARAMS]) WithConcurrentNum(v int64) *EventExecute[PARAMS] {

	e.mu.Lock()
	defer e.mu.Unlock()

	e.executeConcurrentLimit = v
	return e
}

// RegisterExecute注册一个执行单元
// 返回分配的事件号
func RegisterExecute[PARAMS any](execDo func(params *Params[PARAMS])) *EventExecute[PARAMS] {

	// 构造执行单元
	ee := &EventExecute[PARAMS]{
		notifyOverCountToExecute: 1,
		executeConcurrentLimit:   1,
		execDo:                   execDo,
		refCount:                 1,
		// recoverDo:                recoverDo,
	}

	return ee
}

// RefCount 引用计数, 如果引用为0, 则不触发. 返回当前的引用计数
func (exec *EventExecute[PARAMS]) RefCount(count int64) int64 {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	exec.refCount += count
	if exec.refCount < 0 {
		exec.refCount = 0
	}
	return exec.refCount
}

// Notify用于通知触发执行
// 根据事件号查找执行单元并检查通知次数
// 达到指定次数则触发goroutine异步执行
func (exec *EventExecute[PARAMS]) Notify(params PARAMS) {

	// 原子加1并检查通知次数

	exec.mu.Lock()
	defer exec.mu.Unlock()

	if exec.refCount <= 0 {
		return
	}

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

					exec.mu.Lock()
					recoverDo := exec.recoverDo
					exec.mu.Unlock()

					// 调用恢复函数
					if recoverDo != nil {
						recoverDo(ierr)
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
