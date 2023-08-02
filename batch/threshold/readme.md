# Threshold Executor

Threshold Executor是一个基于阈值的批量执行器。它支持当数据积累到一定阈值或周期到达时触发执行。

## 特性

- 数量阈值触发执行
- 时间周期触发执行
- 数量阈值和周期触发可配置
- 支持指定批量执行和周期执行回调函数
- 自动批量处理数据
- 阈值达到时主动推送
- 可配置恢复处理函数

## 用法

```go
executor := NewThresholdExecutor(handler)
```

- handler为每次执行的回调函数 

### 配置

可配置批量阈值、周期等:

```go
executor.WithBatchSize(1000)  
executor.WithPeriodic(time.Second)
```

也可分别配置数量阈值和周期的回调函数:

```go
executor.WithBatchSizeHandler(batchHandler)
executor.WithPeriodicHandler(periodicHandler)
```

### 收集数据

```go
executor.Collect(data)
```

达到阈值时会自动执行处理。

### 执行

```go
executor.AsyncExecute() 
```

启动执行器。

### 控制

```go
executor.Stop()
```

可安全停止执行器。

欢迎提意见或PR帮助改进!