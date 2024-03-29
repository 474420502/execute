package triggered

import (
	"log"
	"runtime"
	"sync"

	"github.com/474420502/execute/utils"
)

// EventExecute封装了一个执行单元, 默认是不启动,需要Notify通知触发. 属于被动触发式
// 包含执行函数,恢复函数和相关控制参数
type EventExecute[ITEM any] struct {
	sub *eventExecuteSub[ITEM]
}

type eventExecuteSub[ITEM any] struct {
	loopExecuteOnce utils.Once
	stopChan        chan struct{}
	stopOnce        utils.OnceNoWait

	itemsChan chan ITEM

	shared Shared
	// 要执行的函数
	execDo func(params *Items[ITEM])
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

type Items[T any] struct {
	Value  []T
	Shared *Shared
}

func (e *EventExecute[ITEM]) WithShared(v any) *EventExecute[ITEM] {
	// 加锁

	e.sub.shared.SetValue(v)
	return e
}

// RegisterExecute注册一个执行单元
// 返回分配的事件号
func RegisterExecute[ITEM any](execDo func(items *Items[ITEM])) *EventExecute[ITEM] {

	// 构造执行单元
	exec := &EventExecute[ITEM]{
		sub: &eventExecuteSub[ITEM]{
			execDo:    execDo,
			stopChan:  make(chan struct{}, 1),
			itemsChan: make(chan ITEM, 1024),
		},
	}

	exec.loopExecute()

	runtime.SetFinalizer(exec, func(ee *EventExecute[ITEM]) {
		// 停止循环执行
		ee.Close()
	})

	return exec
}

type Config[ITEM any] struct {
	ItemsChanSize uint64
	ExecuteDo     func(items *Items[ITEM]) // require
}

// RegisterExecute注册一个执行单元
// 返回分配的事件号
func RegisterExecuteEx[ITEM any](config *Config[ITEM]) *EventExecute[ITEM] {

	// 构造执行单元
	exec := &EventExecute[ITEM]{
		sub: &eventExecuteSub[ITEM]{
			execDo:    config.ExecuteDo,
			stopChan:  make(chan struct{}, 1),
			itemsChan: make(chan ITEM, config.ItemsChanSize),
		},
	}

	exec.loopExecute()

	runtime.SetFinalizer(exec, func(ee *EventExecute[ITEM]) {
		// 停止循环执行
		ee.Close()
	})

	return exec
}

// 关闭整个触发器
func (exec *EventExecute[ITEM]) Close() {
	exec.sub.stopOnce.Do(func() {
		close(exec.sub.stopChan)
		close(exec.sub.itemsChan)
	})

}

func (exec *EventExecute[ITEM]) loopExecute() {
	sub := exec.sub
	exec.sub.loopExecuteOnce.Do(func() {

		go func() {

			var items []ITEM

			for {
				select {
				case <-sub.stopChan:
					// 收到停止信号，退出循环
					return
				case item := <-sub.itemsChan:
					// log.Println(" param := <-exec.params 1 ")
					items = append(items, item)

					func() {
						for {

							select {
							case item := <-sub.itemsChan:
								// log.Println(" param := <-exec.params 2 ")
								items = append(items, item)
							default:
								func() {
									if len(items) == 0 {
										return
									}
									// recover保护
									defer func() {
										if ierr := recover(); ierr != nil {
											log.Println(ierr)
										}
									}()

									// 执行已注册函数
									sub.execDo(&Items[ITEM]{
										Shared: &sub.shared,
										Value:  items[:],
									})
									items = items[:0]
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
func (exec *EventExecute[ITEM]) Notify(item ITEM) {
	exec.sub.itemsChan <- item
}
