package basic

import "context"

// Context 是 context.Context 的别名, 让上层无需重复导入 context.
type Context = context.Context

// 执行器的错误集合. 使用 errors.Is 进行判断.
var (
	// ErrClosed 表示执行器已关闭, 不再接收数据.
	ErrClosed = errSentinel("execute: executor is closed")
	// ErrQueueFull 表示非阻塞收集时缓冲区已满.
	ErrQueueFull = errSentinel("execute: queue is full")
	// ErrStaleCollector 表示使用了已过期(被 Close 取代)的收集句柄.
	ErrStaleCollector = errSentinel("execute: collector is stale")
	// ErrPaused 表示执行器已暂停, 数据会被缓存但不会执行.
	ErrPaused = errSentinel("execute: executor is paused")
)

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
