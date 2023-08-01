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

	items []ITEM
	mu    sync.Mutex

	// 要执行的函数
	execDo func(i int, item ITEM)

	// 错误恢复函数
	recoverDo func(ierr any)

	once *sync.Once

	// 是否打印恢复日志
	isShowLog  atomic.Bool
	execStatus atomic.Int32
}

func NewConcurrentExecute[ITEM any](execDo func(i int, item ITEM)) *ConcurrentExecute[ITEM] {
	e := &ConcurrentExecute[ITEM]{
		periodic: time.Millisecond * 100,
		execDo:   execDo,
		once:     &sync.Once{},
	}

	return e
}

func (pe *ConcurrentExecute[ITEM]) reocverExecDo() {
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
}

func (pe *ConcurrentExecute[ITEM]) batchHandler() {

	pe.mu.Lock()
	if len(pe.items) == 0 {
		pe.mu.Unlock()
		return
	}
	items := pe.items[:]
	pe.items = pe.items[:0]
	pe.mu.Unlock()

	for i, item := range items {
		func(i int, item ITEM) {
			// recover保护
			defer pe.reocverExecDo()
			pe.execDo(i, item)
		}(i, item)
	}

	// log.Println(len(items), "over")
}

func (pe *ConcurrentExecute[ITEM]) asyncExecuteConcurrent() {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	// 定期并发处理
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
			per := pe.periodic
			pe.mu.Unlock()

			go pe.batchHandler()
			time.Sleep(per)

		}
	})
}

func (pe *ConcurrentExecute[ITEM]) WithRecover(recoverDo func(ierr any)) *ConcurrentExecute[ITEM] {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	pe.recoverDo = recoverDo
	return pe
}

func (pe *ConcurrentExecute[ITEM]) WithPeriodic(per time.Duration) *ConcurrentExecute[ITEM] {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	pe.periodic = per
	return pe
}

func (pe *ConcurrentExecute[ITEM]) AsyncExecute() *ConcurrentExecute[ITEM] {
	pe.asyncExecuteConcurrent()
	return pe
}

// Collect 收集数据
func (pe *ConcurrentExecute[ITEM]) Collect(item ITEM) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.items = append(pe.items, item)
}

// Stop 停止执行
func (pe *ConcurrentExecute[ITEM]) Stop() {
	pe.execStatus.Store(0)
}
