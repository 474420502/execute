// Package basic 定义通用任务执行器的公共契约: 执行器接口、处理器、状态与指标.
//
// 设计理念: "数据收集"与"数据执行"解耦. 上层业务只负责 Collect 数据,
// 由执行器决定何时、以多大批次、以多少并发去执行; 执行策略由具体实现
// (batch/periodic、batch/threshold、triggered)决定.
package basic

import (
	"time"
)

// Duration 是 time.Duration 的别名, 便于上层在不引入 time 包的情况下书写回调签名.
type Duration = time.Duration

// Executor 是所有执行器的公共契约.
//
// 生命周期:
//
//	Created --Start/Collect--> Running --Pause--> Paused --Resume--> Running
//	Running/Paused --Close--> Closing --(在途任务结束)--> Stopped
//
// 实现必须保证:
//   - 所有已导出的方法都可并发调用;
//   - Close 幂等;
//   - 关闭后 Collect 不再 panic, 而是返回错误.
type Executor[ITEM any] interface {
	// Collect 收集一条数据. 当内部缓冲区满时可能阻塞(背压), 关闭后返回错误.
	Collect(item ITEM) error
	// TryCollect 非阻塞地收集一条数据, 缓冲区满时返回 ErrQueueFull.
	TryCollect(item ITEM) error
	// Flush 阻塞地等待缓冲区中被当前批次遗漏的数据执行完成.
	Flush() error
	// Start 显式启动执行循环(通常无需调用, Collect 会自动启动).
	Start()
	// Pause 暂停执行: 仍可收集数据, 但不会触发处理.
	Pause()
	// Resume 从暂停中恢复.
	Resume()
	// Close 优雅关闭: 停止接收新数据, 等待在途任务与残留数据执行完成.
	// 该方法是幂等的, 且不阻塞调用方等待执行结束(需要同步等待请使用 Wait).
	Close()
	// Wait 阻塞等待执行器完全停止, ctx 用于超时控制.
	// 返回 nil 表示已完全停止, 返回 ctx.Err() 表示等待超时.
	Wait(ctx Context) error
	// Status 返回当前生命周期状态.
	Status() Status
	// Stats 返回运行指标快照.
	Stats() Stats
}
