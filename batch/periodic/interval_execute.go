package periodic

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// ExecuteInterval 时间间隔执行
type ExecuteInterval[ITEM any] struct {
	periodic time.Duration // 周期的时间

	items []ITEM
	mu    sync.Mutex

	// 要执行的函数
	execDo func(i int, item ITEM)

	// 错误恢复函数
	recoverDo func(ierr any)

	once *sync.Once

	// 是否打印恢复日志
	isShowLog  atomic.Bool
	execStatus atomic.Uint64
	stopSignal chan struct{}
}

func NewExecuteInterval[ITEM any](execDo func(i int, item ITEM)) *ExecuteInterval[ITEM] {
	e := &ExecuteInterval[ITEM]{
		periodic:   time.Millisecond * 100,
		execDo:     execDo,
		once:       &sync.Once{},
		stopSignal: make(chan struct{}),
	}
	e.asyncExecuteInterval()
	return e
}

func (pe *ExecuteInterval[ITEM]) batchHandler() {

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

func (pe *ExecuteInterval[ITEM]) asyncExecuteInterval() {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	//  总执行时间 = 周期时间 + 执行时间
	go pe.once.Do(func() {
		pe.execStatus.Store(1)
		defer func() {
			pe.mu.Lock()
			pe.once = &sync.Once{}
			pe.mu.Unlock()
			pe.execStatus.CompareAndSwap(1, 0)
		}()

		for pe.execStatus.Load() != 0 {
			pe.mu.Lock()
			periodic := pe.periodic
			pe.mu.Unlock()

			pe.batchHandler()
			time.Sleep(periodic)
		}
	})
}

func (pe *ExecuteInterval[ITEM]) AsyncExecute() *ExecuteInterval[ITEM] {
	pe.asyncExecuteInterval()
	return pe
}

func (pe *ExecuteInterval[ITEM]) WithPeriodic(per time.Duration) *ExecuteInterval[ITEM] {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	pe.periodic = per
	return pe
}

func (pe *ExecuteInterval[ITEM]) WithRecover(recoverDo func(ierr any)) *ExecuteInterval[ITEM] {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	pe.recoverDo = recoverDo
	return pe
}

// Collect 收集数据
func (pe *ExecuteInterval[ITEM]) Collect(item ITEM) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.items = append(pe.items, item)
}

// Stop 停止执行
func (pe *ExecuteInterval[ITEM]) Stop() {
	pe.execStatus.Store(0)
}
