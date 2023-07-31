package periodic

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// PeriodicExecute 周期性处理
type PeriodicExecute[ITEM any] struct {
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
	periodic   time.Duration
}

type ModeExecute int

const (
	_ ModeExecute = iota

	ModeInterval   // 时间间隔执行
	ModeCompensate // 时间补偿执行
	ModeConcurrent // 定期并发
)

func (pe *PeriodicExecute[ITEM]) AsyncExecute(m ModeExecute) {
	switch m {
	case ModeInterval:
		pe.asyncExecuteInterval()
	case ModeCompensate:
		pe.asyncExecuteCompensate()
	case ModeConcurrent:
		pe.asyncExecuteConcurrent()
	}
}

func (pe *PeriodicExecute[ITEM]) batchHandler() {

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

					// 调用恢复函数
					if pe.recoverDo != nil {
						pe.recoverDo(ierr)
					}
				}
			}()

			pe.execDo(i, item)
		}()
	}
}

func (pe *PeriodicExecute[ITEM]) asyncExecuteConcurrent() {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	// 定期并发处理
	go pe.once.Do(func() {
		for pe.execStatus.Load() != 0 {
			time.AfterFunc(pe.periodic, func() {
				go pe.batchHandler()
			})
		}
	})
}

func (pe *PeriodicExecute[ITEM]) asyncExecuteCompensate() {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	// 总执行时间 >= 周期时间 如果执行时间大于周期时间,执行后马上执行
	go pe.once.Do(func() {

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

func (pe *PeriodicExecute[ITEM]) asyncExecuteInterval() {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	//  总执行时间 = 周期时间 + 执行时间
	go pe.once.Do(func() {
		for pe.execStatus.Load() != 0 {
			time.AfterFunc(pe.periodic, func() {
				pe.batchHandler()
			})
		}
	})
}

func (pe *PeriodicExecute[ITEM]) Collect(item *ITEM) {
	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.items = append(pe.items, item)
}
