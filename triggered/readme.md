# Event Executor 事件执行器

**被动触发式** 执行器: 不使用任何定时器, 每次 `Notify` 数据到达时立即执行,
把当时缓冲区中的数据合并成一个批次。天然具备"突发流量批量放大"的能力。

```go
import "github.com/474420502/execute/triggered"
```

## 快速开始

```go
exec := triggered.RegisterExecute[Event](func(items *triggered.Items[Event]) error {
    // items.Value 是本批次数据, 顺序与投递顺序一致
    return push(items.Value)
}).WithShared(newCounter())
defer exec.Wait(nil)

exec.Notify(event)  // 数据到达即执行(队列满时按背压策略阻塞)
```

## 执行语义

- **到达即执行**: 每次 `Notify` 被处理时, 把当时缓冲区中的数据一次性交付。
- **串行执行**: 同一执行器内任意时刻只有一个批次在执行, 因此下游无需额外串行化。
- **突发合并**: 处理器忙碌期间投递的数据会排队, 并在处理器空出来后合并执行。
  这使上游可以放心地"来一条发一条", 由执行器承担合并职责。
- **顺序保持**: 批次内与批次间都保持投递顺序。
- **主动合并**: 连续投递后调用 `Flush()`, 可把缓冲区数据合并为单个批次提交。

## 批次上下文

```go
func(items *triggered.Items[Event]) error {
    items.Value  // []Event, 仅本次调用内有效(需要保留请自行复制)
    items.Shared // *triggered.Share, 从创建起存在、跨批次共享
    items.Len()  // 本批次条数
    items.Seq()  // 批次序号, 从 1 开始严格递增(便于幂等与排障)
    return nil
}
```

## 跨批次共享状态 `Share`

```go
s := triggered.NewShare()

s.Store(v)                  // 原子写
s.Load()                    // 原子读(未设置过返回 nil)
s.CompareAndSwap(old, new)  // 原子 CAS
s.Swap(v)                   // 原子交换并返回旧值

s.SetValue(v)               // 回调风格写
s.Value(func(v any) { ... })// 回调风格读(回调在锁内执行, 不得嵌套调用 Value/SetValue)
```

一次性构造:

```go
exec := triggered.RegisterExecuteEx(&triggered.Config[Event]{
    ItemsChanSize: 4096,                      // Notify 队列容量
    ExecuteDo:     handler,                   // 必填
    Shared:        triggered.NewShare(),
    Recover:       func(ierr any) { log.Println(ierr) },
    Backpressure:  engine.BackpressureDrop,   // 队列满时丢弃而不是阻塞
    MaxPending:    1 << 20,
    Name:          "event-push",
})
```

## 生命周期

```go
exec.Notify(e)     // 阻塞投递(队列满时按背压策略)
exec.TryNotify(e)  // 非阻塞投递, 队列满返回 basic.ErrQueueFull 并计数
exec.Flush()       // 立即把缓冲区数据合并为一个批次执行
exec.Close()       // 幂等
exec.Wait(ctx)     // 等待在途批次结束
exec.Stats()       // 指标: 接收/处理/失败/丢弃/在途
```

`Close()` 之后 `Notify/TryNotify` 返回 `basic.ErrClosed`, **不会 panic**。

## 适用与不适用

- 适合: 事件驱动流水线、通知推送、需要"来一条处理一条且自动合并"的聚合场景。
- 不适合: 需要严格按固定节拍执行 —— 请使用 `batch/periodic`;
  需要"攒够 N 条再执行" —— 请使用 `batch/threshold`。
