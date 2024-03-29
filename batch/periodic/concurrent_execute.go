package periodic

import (
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/474420502/execute/utils"
)

// ExecuteCompensate 时间补偿执行, chan size必须设置足够大, 超过就阻塞. 防止内存溢出
// 时间补偿, 只要时间差不够就必须等到时间差
type ConcurrentExecute[ITEM any] struct {
	sub *concurrentExecuteSub[ITEM]
}

type concurrentExecuteSub[ITEM any] struct {
	periodic      atomic.Int64
	concurrentNum atomic.Uint64

	// 要执行的函数
	execDo func(item ITEM)

	loopExecuteOnce sync.Once
	stopChan        chan struct{}
	stopOnce        utils.OnceNoWait
	itemsChan       chan ITEM
}

func NewConcurrentExecute[ITEM any](execDo func(item ITEM)) *ConcurrentExecute[ITEM] {
	e := &ConcurrentExecute[ITEM]{
		sub: &concurrentExecuteSub[ITEM]{
			// periodic: time.Millisecond * 100,
			itemsChan: make(chan ITEM, 1<<16),
			execDo:    execDo,
		},
	}
	e.sub.periodic.Store(int64(time.Millisecond) * 100)
	e.sub.concurrentNum.Store(uint64(runtime.NumCPU()))

	e.loopExecute()

	runtime.SetFinalizer(e, func(ee *ConcurrentExecute[ITEM]) {
		// 停止循环执行
		ee.Close()
	})

	return e
}

func (pe *ConcurrentExecute[ITEM]) WithPeriodic(per time.Duration) *ConcurrentExecute[ITEM] {

	pe.sub.periodic.Store(int64(per))
	return pe
}

// Collect 收集数据
func (exec *ConcurrentExecute[ITEM]) Collect(item ITEM) {
	exec.sub.itemsChan <- item
}

// Stop 停止执行
func (exec *ConcurrentExecute[ITEM]) Close() {
	exec.sub.stopOnce.Do(func() {
		close(exec.sub.stopChan)
		close(exec.sub.itemsChan)
	})

}

func (exec *ConcurrentExecute[ITEM]) loopExecute() {
	sub := exec.sub
	sub.loopExecuteOnce.Do(func() {

		// 创建工作池
		pool := make(chan struct{}, sub.concurrentNum.Load())
		for i := uint64(0); i < sub.concurrentNum.Load(); i++ {
			pool <- struct{}{}
		}

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

									curItems := items[:]
									items = items[:0]

									// 获取工作池的token
									<-pool

									// #1 这里想添加 协程序控制数量,结合整个结构,应该怎么实现最好
									go func() {
										// recover保护
										defer func() {
											if ierr := recover(); ierr != nil {
												log.Println(ierr)
											}
											// 释放token
											pool <- struct{}{}
										}()

										for _, item := range curItems {
											sub.execDo(item)
										}

									}()

									periodic := time.Duration(sub.periodic.Load())
									time.Sleep(periodic)
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

// // ConcurrentExecute 定期并发
// type ConcurrentExecute[ITEM any] struct {
// 	periodic time.Duration // 周期的时间

// 	items []ITEM
// 	mu    sync.Mutex

// 	// 要执行的函数
// 	execDo func(i int, item ITEM)

// 	// 错误恢复函数
// 	recoverDo func(ierr any)

// 	once *sync.Once

// 	// 是否打印恢复日志
// 	isShowLog  atomic.Bool
// 	execStatus atomic.Int32
// }

// func NewConcurrentExecute[ITEM any](execDo func(i int, item ITEM)) *ConcurrentExecute[ITEM] {
// 	e := &ConcurrentExecute[ITEM]{
// 		periodic: time.Millisecond * 100,
// 		execDo:   execDo,
// 		once:     &sync.Once{},
// 	}

// 	return e
// }

// func (pe *ConcurrentExecute[ITEM]) reocverExecDo() {
// 	if ierr := recover(); ierr != nil {

// 		// 打印错误堆栈
// 		if pe.isShowLog.Load() {
// 			log.Println(ierr)
// 		}

// 		pe.mu.Lock()
// 		recoverDo := pe.recoverDo
// 		pe.mu.Unlock()

// 		// 调用恢复函数
// 		if recoverDo != nil {
// 			recoverDo(ierr)
// 		}
// 	}
// }

// func (pe *ConcurrentExecute[ITEM]) batchHandler() {

// 	pe.mu.Lock()
// 	if len(pe.items) == 0 {
// 		pe.mu.Unlock()
// 		return
// 	}
// 	items := pe.items[:]
// 	pe.items = pe.items[:0]
// 	pe.mu.Unlock()

// 	for i, item := range items {
// 		func(i int, item ITEM) {
// 			// recover保护
// 			defer pe.reocverExecDo()
// 			pe.execDo(i, item)
// 		}(i, item)
// 	}

// 	// log.Println(len(items), "over")
// }

// func (pe *ConcurrentExecute[ITEM]) asyncExecuteConcurrent() {
// 	pe.mu.Lock()
// 	defer pe.mu.Unlock()

// 	// 定期并发处理
// 	go pe.once.Do(func() {
// 		pe.execStatus.Store(1)
// 		defer func() {
// 			pe.mu.Lock()
// 			pe.once = &sync.Once{}
// 			pe.mu.Unlock()
// 			pe.execStatus.CompareAndSwap(1, 0)
// 		}()

// 		for pe.execStatus.Load() != 0 {
// 			pe.mu.Lock()
// 			per := pe.periodic
// 			pe.mu.Unlock()

// 			go pe.batchHandler()
// 			time.Sleep(per)

// 		}
// 	})
// }

// func (pe *ConcurrentExecute[ITEM]) WithRecover(recoverDo func(ierr any)) *ConcurrentExecute[ITEM] {
// 	pe.mu.Lock()
// 	defer pe.mu.Unlock()

// 	pe.recoverDo = recoverDo
// 	return pe
// }

// func (pe *ConcurrentExecute[ITEM]) WithPeriodic(per time.Duration) *ConcurrentExecute[ITEM] {
// 	pe.mu.Lock()
// 	defer pe.mu.Unlock()

// 	pe.periodic = per
// 	return pe
// }

// func (pe *ConcurrentExecute[ITEM]) AsyncExecute() *ConcurrentExecute[ITEM] {
// 	pe.asyncExecuteConcurrent()
// 	return pe
// }

// // Collect 收集数据
// func (pe *ConcurrentExecute[ITEM]) Collect(item ITEM) {
// 	pe.mu.Lock()
// 	defer pe.mu.Unlock()
// 	pe.items = append(pe.items, item)
// }

// // Stop 停止执行
// func (pe *ConcurrentExecute[ITEM]) Stop() {
// 	pe.execStatus.Store(0)
// }
