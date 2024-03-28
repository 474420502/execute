package triggered

import (
	"log"
	"runtime"
	"sync"

	"github.com/474420502/execute/utils"
)

// EventExecute封装了一个执行单元, 默认是不启动,需要Notify通知触发. 属于被动触发式
// 包含执行函数,恢复函数和相关控制参数
type EventExecute[PARAMS any] struct {
	sub *eventExecuteSub[PARAMS]
}

type eventExecuteSub[PARAMS any] struct {
	loopExecuteOnce utils.Once
	stopChan        chan struct{}
	stopOnce        utils.OnceNoWait

	params chan PARAMS

	shared Shared
	// 要执行的函数
	execDo func(params *Params[PARAMS])
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
	Value  []T
	Shared *Shared
}

func (e *EventExecute[PARAMS]) WithShared(v any) *EventExecute[PARAMS] {
	// 加锁

	e.sub.shared.SetValue(v)
	return e
}

// RegisterExecute注册一个执行单元
// 返回分配的事件号
func RegisterExecute[PARAMS any](execDo func(params *Params[PARAMS])) *EventExecute[PARAMS] {

	// 构造执行单元
	exec := &EventExecute[PARAMS]{
		sub: &eventExecuteSub[PARAMS]{
			execDo:   execDo,
			stopChan: make(chan struct{}, 1),
			params:   make(chan PARAMS, 1024),
		},
	}

	exec.loopExecute()

	runtime.SetFinalizer(exec, func(ee *EventExecute[PARAMS]) {
		// 停止循环执行
		ee.Close()
	})

	return exec
}

// 关闭整个触发器
func (exec *EventExecute[PARAMS]) Close() {
	exec.sub.stopOnce.Do(func() {
		close(exec.sub.stopChan)
		close(exec.sub.params)
	})

}

func (exec *EventExecute[PARAMS]) loopExecute() {
	sub := exec.sub
	exec.sub.loopExecuteOnce.Do(func() {

		go func() {

			var params []PARAMS

			for {
				select {
				case <-sub.stopChan:
					// 收到停止信号，退出循环
					return
				case param := <-sub.params:
					// log.Println(" param := <-exec.params 1 ")
					params = append(params, param)

					func() {
						for {

							select {
							case param := <-sub.params:
								// log.Println(" param := <-exec.params 2 ")
								params = append(params, param)
							default:
								func() {
									if len(params) == 0 {
										return
									}
									// recover保护
									defer func() {
										if ierr := recover(); ierr != nil {
											log.Println(ierr)
										}
									}()

									// 执行已注册函数
									sub.execDo(&Params[PARAMS]{
										Shared: &sub.shared,
										Value:  params[:],
									})
									params = params[:0]
								}()
								return
							}
						}

					}()

				}

			}

		}()
	})
}

// Notify用于通知触发执行
// 根据事件号查找执行单元并检查通知次数
// 达到指定次数则触发goroutine异步执行
func (exec *EventExecute[PARAMS]) Notify(params PARAMS) {
	exec.sub.params <- params
}
