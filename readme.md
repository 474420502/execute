# 通用任务执行器

这个模块实现了多种通用的任务执行器,可以用来执行定时任务、周期性任务、基于触发的任务等。

## 执行器类型

- Periodic Executor - 周期执行器
- Threshold Executor - 阈值执行器 
- Event Executor - 事件执行器

## 共性

- 支持异步并发执行
- 可配置执行周期/阈值
- 数据收集与执行解耦
- 执行错误处理
- 执行控制(开始/停止)

## Periodic Executor

定时或固定间隔执行任务。

**特性**

- 间隔循环执行
- 执行时间补偿
- 并发批量执行

**用法**

```go
executor := NewPeriodicExecutor(handler)
executor.Collect(data) 
executor.AsyncExecute()
```

## Threshold Executor 

数据积累到阈值或周期达到时触发执行。

**特性**

- 数量阈值触发
- 周期触发
- 自动批量处理

**用法**

```go
executor := NewThresholdExecutor(handler)
executor.Collect(data)
executor.AsyncExecute() 
```

## Event Executor

由外部事件通知触发执行。

**特性** 

- 事件触发执行
- 检查阈值与限流
- 错误恢复

**用法**

```go
exec := RegisterExecute(handler)
exec.Notify(params)
```

欢迎提出改进意见!