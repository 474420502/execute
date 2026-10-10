# Periodic Executor 周期执行器

按时间驱动的执行器, 提供三种执行语义。三者共享同一套采集、生命周期与观测能力,
差异只在 **何时执行** 与 **如何执行**。

```go
import "github.com/474420502/execute/batch/periodic"
```

## 三种模式

| 构造 | 模式 | 执行语义 | 适用场景 |
| --- | --- | --- | --- |
| `NewInterval(handler, d)` | `Interval` | 每 `d` 把缓冲区数据作为一个批次执行; 批次超时则丢拍 | 常规批量落库、定时聚合 |
| `NewCompensate(handler, d)` | `Compensate` | 按截止时刻对齐, 批次耗时超过 `d` 时立即开始下一批, 长期平均速率 = `1/d` | 需要稳定节拍(例如采样、限速器) |
| `NewConcurrent(handler, d, n)` | `Concurrent` | 每 `d` 取走一批, 交给最多 `n` 个 worker 并发执行 | 单批耗时长、想用并发提高吞吐 |

```go
// 每 100ms 批量写入一次
exec := periodic.NewInterval[Order](func(batch []Order) error {
    return db.BatchInsert(batch)
}, 100*time.Millisecond)
defer exec.Wait(nil)

exec.Collect(order1)
exec.Collect(order2)
```

## 关键行为

- **不产生空批次**: 缓冲区为空时不会调用 `Handler`。
- **顺序保持**: 同一批次内数据顺序与投递顺序一致, 且不会重复。
- **数据切片复用**: `Handler` 的入参仅在本次调用内有效, 需要保留请 `slices.Clone`。
- **`Flush()`**: 立即执行当前缓冲区。`Concurrent` 模式下不等待已派发的 worker,
  需要完全同步请用 `Wait(ctx)`。
- **`Compensate` 的慢启动语义**: 数据到达时若距上次执行不足一个周期, 会等到截止时刻
  才执行, 因此不会超过上游速率, 也不会累积漂移。

## 配置

```go
exec.WithPeriodic(50 * time.Millisecond)  // 热更新周期(下一个网格点生效)
exec.WithConcurrentNum(8)                 // 仅 Concurrent: 热更新并发上限
exec.WithMaxPending(1 << 20)              // 缓冲区积压上限, 超出即丢弃并计数
exec.WithBackpressure(periodic.BackpressureDrop) // 队列满时丢弃而不是阻塞
exec.WithRecover(func(ierr any) { log.Println(ierr) })
exec.WithOnExecute(func(ev periodic.ExecuteEvent) { metrics.Observe(ev) })

// 需要在创建时精细控制队列容量/背压/名称, 使用配置结构
exec := periodic.NewFromConfig(periodic.Config[Order]{
    Handler:      handler,
    Mode:         periodic.ModeConcurrent,
    Interval:     100 * time.Millisecond,
    Concurrency:  8,
    QueueSize:    1 << 12, // 采集队列容量: 越小, 背压越早作用于上游
    Backpressure: periodic.BackpressureBlock,
    OnExecute:    func(ev periodic.ExecuteEvent) { metrics.Observe(ev) },
    Name:         "order-writer",
})
```

## 生命周期

```go
exec.Start()        // 显式启动(通常无需调用, 首次 Collect 会自动启动)
exec.Pause()        // 暂停: 继续接收数据但不触发执行(返回后立即生效)
exec.Resume()
exec.Flush()        // 立即执行缓冲区
exec.Close()        // 优雅关闭, 幂等
exec.Wait(ctx)      // 等待在途任务与残留数据处理完毕
exec.Status()       // created / running / paused / closing / stopped
exec.Stats()        // 接收/处理/成功/失败/丢弃/在途 + 最近错误
```

关闭后 `Collect` 返回 `periodic.ErrClosed`(等价于 `basic.ErrClosed`), 不会 panic。

## 并发上限如何被保证

`Concurrent` 模式用信号量限制"同时在途的批次数"。并发上限可以在运行期调整:

```go
exec.WithConcurrentNum(16) // 扩容: 立即生效
exec.WithConcurrentNum(1)  // 缩容: 不剥夺已在执行的批次, 空闲后自动收敛
```

`TestConcurrentBoundedParallelism` 会验证在处理阻塞的情况下, 进入处理器的批次数量
绝不超过配置上限。
