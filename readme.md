# 通用任务执行器

`github.com/474420502/execute` 是一组通用任务执行器: **把"数据采集"与"数据执行"解耦**,
业务侧只负责投递数据, 由执行器决定 **何时执行、以多大批次执行、用多少并发执行**。

[![CI](https://github.com/474420502/execute/actions/workflows/ci.yml/badge.svg)](https://github.com/474420502/execute/actions/workflows/ci.yml)

```go
exec := periodic.NewInterval[Order](func(batch []Order) error {
    return db.BatchInsert(batch) // 每 100ms 落库一次, 业务侧不用关心定时器
}, 100*time.Millisecond)
defer exec.Wait(nil)

// 业务代码任意位置、任意 goroutine 投递即可
exec.Collect(order)
```

---

## 三类执行器

| 执行器 | 包 | 触发时机 | 典型场景 |
| --- | --- | --- | --- |
| 周期执行器 | `batch/periodic` | 固定周期 / 固定速率 / 周期并发 | 批量落库、聚合上报、定时刷新 |
| 阈值执行器 | `batch/threshold` | 数量达到阈值立即执行, 周期兜底 | 日志批量投递、高吞吐削峰 |
| 事件执行器 | `triggered` | 数据到达即执行(被动触发) | 事件驱动、通知推送、流水线触发 |

三者共享同一套引擎(`basic/engine`), 因此 **生命周期、背压策略、指标、热更新、panic 隔离**
的行为完全一致。

---

## 特性

- **采集与执行解耦**: `Collect/Notify` 只入队; 执行策略由执行器决定。
- **不会 panic 的关闭语义**: `Close()` 幂等, 关闭后投递返回 `basic.ErrClosed`。
- **可同步等待**: `Wait(ctx)` 等待在途任务与残留数据处理完毕, 不丢数据。
- **背压可控**: 队列满时默认阻塞, 也可切换为"丢弃并计数"(`engine.BackpressureDrop`)。
- **运行期热更新**: 周期、阈值、并发上限、背压策略、处理器均可随时修改, 下一轮生效。
- **panic 隔离**: 处理器 panic 会被捕获、计数并回调 `RecoverFunc`, 执行循环不中断。
- **可观测**: `Stats()` 提供接收/处理/成功/失败/丢弃/在途等指标与最近错误记录。
- **确定性测试**: 周期相关测试基于 `testing/synctest` 虚拟时钟, 不依赖真实时间。

---

## 快速开始

```bash
go get github.com/474420502/execute
```

### 1. 周期执行器

```go
// 每 100ms 把缓冲区中的数据作为一个批次执行
exec := periodic.NewInterval[Item](handler, 100*time.Millisecond)

// 每个周期把缓冲区交给最多 4 个 worker 并发执行
exec := periodic.NewConcurrent[Item](handler, 100*time.Millisecond, 4)

// 固定速率补偿: 批次耗时超过周期时不会漂移, 长期平均速率稳定为 1/周期
exec := periodic.NewCompensate[Item](handler, 100*time.Millisecond)
```

`WithPeriodic` / `WithConcurrentNum` 可在运行期热更新:

```go
exec.WithPeriodic(50 * time.Millisecond)
```

### 2. 阈值执行器

```go
exec := threshold.NewThresholdExecute[Item](handler). // handler: 默认处理器(数量阈值路径)
    WithBatchSize(128).                               // 达到 128 条立即执行
    WithPeriodic(time.Second).                         // 周期兜底, 避免低流量饿死
    WithPeriodicHandler(handlerSlow)                   // 可选: 兜底路径使用不同处理器
```

### 3. 事件执行器

```go
exec := triggered.RegisterExecute[Event](func(items *triggered.Items[Event]) error {
    // items.Value 是本批次数据; items.Shared 是跨批次共享状态
    return push(items.Value)
})

exec.Notify(event)   // 数据到达即执行; 队列满时按背压策略处理
exec.TryNotify(e)    // 非阻塞版本, 队列满返回 basic.ErrQueueFull
```

### 4. 生命周期与错误处理

```go
if err := exec.Collect(item); err != nil {
    // 仅在执行器已关闭(ErrClosed)或非阻塞投递队列满(ErrQueueFull)时返回错误
}

exec.Pause()        // 暂停执行: 继续接收数据但不触发处理
exec.Resume()
exec.Flush()        // 立即执行当前缓冲区

exec.Close()        // 优雅关闭(幂等, 不阻塞)
_ = exec.Wait(ctx)  // 等待在途任务与残留数据全部结束

stats := exec.Stats()
log.Printf("processed=%d failed=%d dropped=%d inFlight=%d",
    stats.ProcessedItems, stats.FailedItems, stats.DroppedItems, stats.InFlight)
```

---

## 执行语义细则

理解下面几条可以避免绝大多数误用:

1. **批次数据是复用的**
   `Handler` 收到的切片由执行器复用, 仅在这次调用内有效。需要长期持有请自行复制
   (`slices.Clone`)。并发模式下执行器会传入独立切片作为例外, 但不要依赖这一点。

2. **`Flush()` 的边界**
   `Flush()` 保证"此前投递的数据到调用返回时已被处理或正在处理"。
   在 **并发模式** 下, 它不等待已派发的 worker 结束 —— 需要完全同步请调用 `Wait()`。

3. **周期模式不产生空批次**
   缓冲区为空时不会调用 `Handler`; 新增数据最多等待一个周期。

4. **`Compensate` 与 `Interval` 的区别**
   `Interval` 到点执行, 批次超时会丢拍(与 `time.Ticker` 一致);
   `Compensate` 会按"截止时刻"对齐, 批次耗时超过周期时立即开始下一批,
   使长期平均速率稳定在 `1/周期`, 适合对节拍稳定性有要求的场景。

5. **触发模式(triggered)是"到达即执行"**
   每次 `Notify` 到达时, 把当时缓冲区中的数据合并成一批立即执行。
   处理器忙碌期间积压的数据会在处理器空出来后合并执行, 因此突发流量天然被批量放大。

6. **背压**
   队列满时默认阻塞投递(形成上游背压); 若业务更在意延迟, 可切换丢弃策略并配合
   `Stats().DroppedItems` / `OnDrop` 观测:

   ```go
   exec.Configure(engine.WithBackpressure[int](engine.BackpressureDrop))
   ```

7. **panic 与错误**
   `Handler` 返回 `error` 会被记录为一次失败执行; panic 会被捕获, 触发 `RecoverFunc`
   并记录堆栈, 执行循环继续运行。因此单个坏数据不会拖垮整个执行器。

---

## 自定义执行器

只需要实现 `basic.Executor` 契约即可复用整套采集、生命周期与观测能力:

```go
var _ basic.Executor[int] = (*MyExecutor[int])(nil)
```

参考 `batch/periodic`、`batch/threshold`、`triggered` 的实现: 它们都是
`basic/engine` 之上的薄封装。

---

## 目录结构

```
basic/            公共契约: Executor、Handler、Status、Stats、Semaphore、错误定义
basic/engine/     执行引擎: 采集队列、dispatcher、批次路由、并发控制、指标(内部实现)
batch/periodic/   周期执行器: Interval / Compensate / Concurrent
batch/threshold/  阈值执行器: 数量阈值 + 周期兜底
triggered/        事件执行器: Notify 触发 + 跨批次共享状态
utils/            可复用同步原语: Once / OnceNoWait
```

---

## 开发与测试

```bash
go vet ./...
go test -race ./...              # 全量测试(含竞态检测)
go test -run XXX -bench . ./...  # 基准测试
```

- 周期相关的测试使用 Go 1.25+ 的 `testing/synctest` 虚拟时钟, 因此 **不依赖真实等待**,
  在 CI 上稳定且极快。
- 关键回归用例(历史缺陷)都有对应测试:
  `Close()` 幂等(不再是未初始化 channel)、关闭后投递返回错误、处理器 panic 隔离、
  并发上限不被突破、补偿模式不漂移、`threshold.Collect` 不死锁。

## 环境要求

- Go **1.25+**(使用了泛型类型别名与稳定的 `testing/synctest`; 已在 go1.27 上验证)。

## License

见 [LICENSE](./LICENSE)。
