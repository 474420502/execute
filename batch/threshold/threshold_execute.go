package threshold

import (
	"sync"
	"time"
)

// ThresholdExecute 阈值执行, 超过阈值就执行. 必须调用AsyncExecute才能执行.
// 默认 batchsize 128 periodic 100ms
type ThresholdExecute[ITEM any] struct {
	periodic  time.Duration //  时间
	batchsize int           // batch的数量

	once *sync.Once

	timeSignal chan struct{}
	sizeSignal chan []ITEM

	itemSizeDo     func(i int, item ITEM)
	itemPeriodicDo func(i int, item ITEM)
	itemDo         func(i int, item ITEM)

	recoverDo  func(ierr any)
	stopSignal chan struct{}

	items []ITEM
	mu    sync.Mutex
}

func NewThresholdExecute[ITEM any](itemDo func(i int, item ITEM)) *ThresholdExecute[ITEM] {
	exec := &ThresholdExecute[ITEM]{
		periodic:  time.Millisecond * 100,
		batchsize: 128,

		itemDo: itemDo,
		once:   &sync.Once{},

		timeSignal: make(chan struct{}),
		sizeSignal: make(chan []ITEM),
		stopSignal: make(chan struct{}),
	}
	// exec.AsyncExecute()
	return exec
}

func (exec *ThresholdExecute[ITEM]) getBatch() []ITEM {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	items := exec.items[:]
	exec.items = exec.items[:0]
	return items
}

// AsyncExecute 返回自身. 方便与With设置连用
func (exec *ThresholdExecute[ITEM]) AsyncExecute() *ThresholdExecute[ITEM] {

	go exec.once.Do(func() {
		var itemPeriodicDo func(i int, item ITEM)
		var itemSizeDo func(i int, item ITEM)

		for {

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
	return exec
}

func (pe *ThresholdExecute[ITEM]) WithRecover(recoverDo func(ierr any)) *ThresholdExecute[ITEM] {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	pe.recoverDo = recoverDo
	return pe
}

func (pe *ThresholdExecute[ITEM]) WithBatchSize(bsize int) *ThresholdExecute[ITEM] {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	pe.batchsize = bsize
	return pe
}

func (pe *ThresholdExecute[ITEM]) WithBatchSizeHandler(itemSizeDo func(i int, item ITEM)) *ThresholdExecute[ITEM] {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	pe.itemSizeDo = itemSizeDo
	return pe
}

func (pe *ThresholdExecute[ITEM]) WithPeriodic(per time.Duration) *ThresholdExecute[ITEM] {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	pe.periodic = per
	return pe
}

func (pe *ThresholdExecute[ITEM]) WithPeriodicHandler(itemPeriodicDo func(i int, item ITEM)) *ThresholdExecute[ITEM] {
	pe.mu.Lock()
	defer pe.mu.Unlock()

	pe.itemPeriodicDo = itemPeriodicDo
	return pe
}

// Collect 收集数据
func (exec *ThresholdExecute[ITEM]) Collect(item ITEM) {
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
