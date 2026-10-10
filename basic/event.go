package basic

import "time"

// Time 是 time.Time 的别名, 避免基础包暴露多余导入.
type Time = time.Time

// ExecuteEvent 描述一次批次执行的结果, 用于 OnExecute 观测回调.
//
// 该回调在执行路径上同步调用, 必须保持轻量(仅做计数、打点或投递到独立队列),
// 否则会拖慢执行吞吐.
type ExecuteEvent struct {
	// Items 是本次批次的数据条数.
	Items int
	// Err 是失败原因, nil 表示成功.
	Err error
	// Panicked 表示失败来自 panic 而非 error 返回值.
	Panicked bool
	// Duration 是 Handler 的执行耗时.
	Duration Duration
	// Time 是执行开始时间.
	Time time.Time
}

// OK 返回本次执行是否成功.
func (e ExecuteEvent) OK() bool { return e.Err == nil }

// DropEvent 描述一条因背压策略被丢弃的数据, 用于 OnDrop 观测回调.
type DropEvent struct {
	// At 是丢弃时间.
	At time.Time
	// Reason 是丢弃原因, 例如 "queue-full"、"max-pending"、"not-running".
	Reason string
	// PendingItems 是丢弃时刻缓冲区中的积压条数.
	PendingItems int
}
