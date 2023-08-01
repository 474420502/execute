package periodic

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// ConcurrentExecute 定期并发
type ConcurrentExecute[ITEM any] struct {
	periodic time.Duration // 周期的时间

	items []*ITEM
	mu    sync.Mutex

	// 要执行的函数
	execDo func(i int, item *ITEM)

	// 错误恢复函数
	recoverDo func(ierr any)

	once *sync.Once

	// 是否打印恢复日志
	isShowLog  atomic.Bool
	execStatus atomic.Uint64
	stopSignal chan struct{}
}

func NewConcurrentExecute[ITEM any](periodic time.Duration, execDo func(i int, item *ITEM)) *ConcurrentExecute[ITEM] {
	e := &ConcurrentExecute[ITEM]{
		periodic:   periodic,
		execDo:     execDo,
		once:       &sync.Once{},
		stopSignal: make(chan struct{}),
	}
	e.asyncExecuteConcurrent()
	return e
}

func (pe *ConcurrentExecute[ITEM]) SetRecover(recoverDo func(ierr any)) {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	pe.recoverDo = recoverDo
}

func (pe *ConcurrentExecute[ITEM]) batchHandler() {

	pe.mu.Lock()
	items := pe.items[:]
	pe.items = pe.items[:0]
	pe.mu.Unlock()

	for i, item := range items {
		func() {
			// recover保护
			defer func() {
				if ierr := recover(); ierr != nil {

					// 打印错误堆栈
					if pe.isShowLog.Load() {
						log.Println(ierr)
					}

					pe.mu.Lock()
					recoverDo := pe.recoverDo
					pe.mu.Unlock()

					// 调用恢复函数
					if recoverDo != nil {
						recoverDo(ierr)
					}
				}
			}()

			pe.execDo(i, item)
		}()
	}
}

func (pe *ConcurrentExecute[ITEM]) asyncExecuteConcurrent() {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	// 定期并发处理
	go pe.once.Do(func() {
		defer func() {
			pe.stopSignal <- struct{}{}
		}()

		for pe.execStatus.Load() != 0 {
			time.AfterFunc(pe.periodic, func() {
				go pe.batchHandler()
			})
		}
	})
}

// Collect 收集数据
func (pe *ConcurrentExecute[ITEM]) Collect(item *ITEM) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.items = append(pe.items, item)
}

// Stop 停止执行
func (pe *ConcurrentExecute[ITEM]) Stop() {
	pe.execStatus.Store(0)
	<-pe.stopSignal

	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.once = &sync.Once{}
}
