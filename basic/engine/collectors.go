package engine

import "github.com/474420502/execute/basic"

// Collector 是采集句柄, 是"采集与执行解耦"的载体.
//
// 采集方只需要持有 *Collector 即可持续投递数据, 完全不需要知道执行周期、
// 阈值、并发度与生命周期; 引擎关闭后所有句柄统一返回 basic.ErrClosed.
//
// 句柄本身无状态、可复制(值语义), 可在多个 goroutine 之间共享.
type Collector[ITEM any] struct {
	engine *Engine[ITEM]
}

// Push 收集一条数据. 队列满时按引擎的背压策略处理(默认阻塞等待).
func (c *Collector[ITEM]) Push(item ITEM) error {
	if c == nil || c.engine == nil {
		return basic.ErrStaleCollector
	}
	return c.engine.Collect(item)
}

// TryPush 非阻塞收集一条数据; 队列满时返回 basic.ErrQueueFull.
func (c *Collector[ITEM]) TryPush(item ITEM) error {
	if c == nil || c.engine == nil {
		return basic.ErrStaleCollector
	}
	return c.engine.TryCollect(item)
}

// PushAll 批量收集. 返回第一条失败的错误, 已成功入队的数据不会被回滚.
func (c *Collector[ITEM]) PushAll(items ...ITEM) error {
	for _, item := range items {
		if err := c.Push(item); err != nil {
			return err
		}
	}
	return nil
}

// Engine 返回句柄所属的引擎, 便于访问指标与生命周期方法.
func (c *Collector[ITEM]) Engine() *Engine[ITEM] {
	if c == nil {
		return nil
	}
	return c.engine
}
