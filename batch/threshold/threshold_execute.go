package threshold

import (
	"sync"
	"time"
)

// ThresholdExecute 阈值执行, 超过阈值就执行
type ThresholdExecute[ITEM any] struct {
	periodic  time.Duration //  时间
	batchsize int           // batch的数量

	once *sync.Once

	timeSignal chan struct{}
	sizeSignal chan []*ITEM

	itemSizeDo     func(i int, item *ITEM)
	itemPeriodicDo func(i int, item *ITEM)
	itemDo         func(i int, item *ITEM)

	recoverDo  func(ierr any)
	stopSignal chan struct{}

	items []*ITEM
	mu    sync.Mutex
}

func NewThresholdExecute[ITEM any](periodic time.Duration, itemDo func(i int, item *ITEM)) *ThresholdExecute[ITEM] {
	exec := &ThresholdExecute[ITEM]{
		periodic: periodic,
		itemDo:   itemDo,
		once:     &sync.Once{},

		timeSignal: make(chan struct{}, 1),
		sizeSignal: make(chan []*ITEM, 1),
		stopSignal: make(chan struct{}),
	}
	exec.asyncExecute()
	return exec
}

func (exec *ThresholdExecute[ITEM]) getBatch() []*ITEM {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	items := exec.items[:]
	exec.items = exec.items[:0]
	return items
}

func (exec *ThresholdExecute[ITEM]) asyncExecute() {

	go exec.once.Do(func() {
		var itemPeriodicDo func(i int, item *ITEM)
		var itemSizeDo func(i int, item *ITEM)

		exec.mu.Lock()
		overTimer := time.NewTimer(exec.periodic)
		if exec.itemPeriodicDo != nil {
			itemPeriodicDo = exec.itemPeriodicDo
		} else {
			itemPeriodicDo = exec.itemDo
		}

		if exec.itemSizeDo != nil {
			itemSizeDo = exec.itemSizeDo
		} else {
			itemSizeDo = exec.itemDo
		}
		exec.mu.Unlock()

		for {
			select {
			case <-overTimer.C:
				items := exec.getBatch()
				for i, item := range items {
					itemPeriodicDo(i, item)
				}
			case items := <-exec.sizeSignal:
				for i, item := range items {
					itemSizeDo(i, item)
				}
			case <-exec.stopSignal:
				return
			}
		}
	})

}

func (pe *ThresholdExecute[ITEM]) SetRecover(recoverDo func(ierr any)) {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	pe.recoverDo = recoverDo
}

// Collect 收集数据
func (exec *ThresholdExecute[ITEM]) Collect(item *ITEM) {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	exec.items = append(exec.items, item)
	if len(exec.items) >= exec.batchsize {
		items := exec.items[:]
		exec.items = exec.items[:0]
		exec.sizeSignal <- items
	}
}

// Stop 停止执行
func (pe *ThresholdExecute[ITEM]) Stop() {
	pe.stopSignal <- struct{}{}

	pe.mu.Lock()
	defer pe.mu.Unlock()
	pe.once = &sync.Once{}
}
