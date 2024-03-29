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
type ExecuteCompensate[ITEM any] struct {
	sub *executeCompensateSub[ITEM]
}

type executeCompensateSub[ITEM any] struct {
	periodic atomic.Int64

	// 要执行的函数
	execDo func(item ITEM)

	loopExecuteOnce sync.Once
	stopChan        chan struct{}
	stopOnce        utils.OnceNoWait
	itemsChan       chan ITEM
}

func NewExecuteCompensate[ITEM any](execDo func(item ITEM)) *ExecuteCompensate[ITEM] {
	e := &ExecuteCompensate[ITEM]{
		sub: &executeCompensateSub[ITEM]{
			// periodic: time.Millisecond * 100,
			itemsChan: make(chan ITEM, 1<<16),
			execDo:    execDo,
		},
	}
	e.sub.periodic.Store(int64(time.Millisecond) * 100)

	e.loopExecute()

	runtime.SetFinalizer(e, func(ee *ExecuteCompensate[ITEM]) {
		// 停止循环执行
		ee.Close()
	})

	return e
}

func (pe *ExecuteCompensate[ITEM]) WithPeriodic(per time.Duration) *ExecuteCompensate[ITEM] {

	pe.sub.periodic.Store(int64(per))
	return pe
}

// Collect 收集数据
func (exec *ExecuteCompensate[ITEM]) Collect(item ITEM) {
	exec.sub.itemsChan <- item
}

// Stop 停止执行
func (exec *ExecuteCompensate[ITEM]) Close() {
	exec.sub.stopOnce.Do(func() {
		close(exec.sub.stopChan)
		close(exec.sub.itemsChan)
	})

}

func (exec *ExecuteCompensate[ITEM]) loopExecute() {
	sub := exec.sub
	sub.loopExecuteOnce.Do(func() {

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

									now := time.Now()

									for _, item := range items {
										sub.execDo(item)
									}
									items = items[:0]

									// 时间补偿, 只要时间差不够就必须等到时间差
									subtime := time.Since(now)
									periodic := time.Duration(sub.periodic.Load())
									if subtime < periodic {
										time.Sleep(periodic - subtime)
									}
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
