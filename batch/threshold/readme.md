# Threshold Executor 阈值执行器

**数量阈值 + 周期兜底** 的批量执行器: 数据量够了就立刻执行(低延迟),
数据量不够时由周期兜底执行(不饿死)。

```go
import "github.com/474420502/execute/batch/threshold"
```

## 快速开始

```go
exec := threshold.NewThresholdExecute[Log](func(batch []Log) error {
    return store.BatchInsert(batch) // 达到阈值时执行
}).
    WithBatchSize(128).            // 达到 128 条立即执行
    WithPeriodic(time.Second)      // 不足 128 条时, 最多攒 1 秒

defer exec.Wait(nil)

for _, l := range logs {
    exec.Collect(l)
}
```

## 两条触发路径

| 触发条件 | 使用的处理器 | 说明 |
| --- | --- | --- |
| 缓冲区条数 >= `BatchSize` | `SizeHandler`(默认 `handler`) | 立即执行, 不等周期 |
| 周期到达且缓冲区非空 | `PeriodicHandler`(默认 `handler`) | 只处理不足阈值的残留数据 |
| 显式 `Flush()` | `PeriodicHandler` | 立即把残留数据刷出去 |

可以为两条路径配置不同处理器, 例如"阈值路径走批量写, 兜底路径走小批写 + 告警":

```go
exec := threshold.NewThresholdExecute[Log](bulkInsert).
    WithBatchSize(1000).
    WithPeriodic(time.Second).
    WithBatchSizeHandler(bulkInsert).
    WithPeriodicHandler(func(batch []Log) error {
        log.Printf("兜底提交 %d 条(未达阈值)", len(batch))
        return bulkInsert(batch)
    })
```

## 关键行为

- **采集永不与执行互锁**: `Collect` 只入队, 不会因为处理器慢而与自己死锁;
  处理器慢时表现为队列填满后的上游背压。
- **数据切片复用**: `Handler` 的入参仅在本次调用内有效, 需要保留请 `slices.Clone`。
- **阈值热更新**: `WithBatchSize` 在下一批数据上生效。
- **周期热更新**: `WithPeriodic` 立即按新周期重新计时。
- **默认值**: `BatchSize = 128`, `Periodic = 100ms`。

## 生命周期

`ThresholdExecute` 同时提供两套等价方法:

```go
exec.Stop()      // 兼容用法: Close + Wait, 阻塞到全部结束
exec.Close()     // 优雅关闭(幂等, 不阻塞)
exec.Wait(ctx)   // 同步等待
exec.Collect(x)  // 关闭后返回 threshold/basic.ErrClosed
```

## 数量阈值触发但不立即执行?

仅在 `Concurrent` 并发模式下, 引擎默认把"数量阈值"推迟到下一个节拍统一切分批次,
避免到达速率波动导致并发批次数失控。阈值执行器始终使用"立即执行"语义。
如需在并发模式下改变该行为, 使用 `engine.WithDeferOnThreshold[int](false)` 显式声明。
