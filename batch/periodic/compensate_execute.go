package periodic

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// ExecuteCompensate 时间补偿执行
type ExecuteCompensate[ITEM any] struct {
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

func NewExecuteCompensate[ITEM any](periodic time.Duration, execDo func(i int, item *ITEM)) *ExecuteCompensate[ITEM] {
	e := &ExecuteCompensate[ITEM]{
		periodic:   periodic,
		execDo:     execDo,
		once:       &sync.Once{},
		stopSignal: make(chan struct{}),
	}
	e.asyncExecuteCompensate()
	return e
}

func (pe *ExecuteCompensate[ITEM]) batchHandler() {

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

func (pe *ExecuteCompensate[ITEM]) SetRecover(recoverDo func(ierr any)) {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	pe.recoverDo = recoverDo
}

func (pe *ExecuteCompensate[ITEM]) asyncExecuteCompensate() {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	// 总执行时间 >= 周期时间 如果执行时间大于周期时间,执行后马上执行
	go pe.once.Do(func() {
		defer func() {
			pe.stopSignal <- struct{}{}
		}()

		for pe.execStatus.Load() != 0 {
			now := time.Now()

			pe.batchHandler()

			sub := time.Since(now)
			if sub < pe.periodic {
				time.Sleep(pe.periodic - sub)
			}
		}
	})
}

// Collect 收集数据
func (pe *ExecuteCompensate[ITEM]) Collect(item *ITEM) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.items = append(pe.items, item)
}

// Stop 停止执行
func (pe *ExecuteCompensate[ITEM]) Stop() {
	pe.execStatus.Store(0)
	<-pe.stopSignal

	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.once = &sync.Once{}
}
