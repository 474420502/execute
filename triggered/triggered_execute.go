package triggered

import (
	"log"
	"sync"
	"sync/atomic"
)

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

func (e *EventExecute[PARAMS]) SetRecover(recoverDo func(ierr any)) {
	// 加锁
	e.mu.Lock()
	defer e.mu.Unlock()

	e.recoverDo = recoverDo
}

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
func RegisterExecute[PARAMS any](execDo func(params *Params[PARAMS])) *EventExecute[PARAMS] {

	// 构造执行单元
	ee := &EventExecute[PARAMS]{
		notifyOverCountToExecute: 1,
		executeConcurrentLimit:   1,
		execDo:                   execDo,
		// recoverDo:                recoverDo,
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
