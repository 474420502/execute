# Event Executor

Event Executor是一个基于事件触发的执行器。它可以注册执行函数和错误恢复函数,通过外部事件通知触发执行。

## 特性

- 支持事件通知触发执行
- 执行前检查通知计数阈值
- 可控制最大并发执行数 
- 执行错误恢复保护
- 共享参数传递

## 用法

### 1. 定义执行函数

执行函数签名为:

```go
func(params *Params[T])
```

T是参数类型。

### 2. 注册执行函数

使用RegisterExecute注册:

```go
exec := RegisterExecute[T](execFunc)
```

### 3. 配置(可选)

可以链式配置执行单元:

```go
exec.WithRecover(recoverFunc).WithShared(sharedData).WithConcurrentNum(10) 
```

- WithRecover 指定错误恢复函数
- WithShared 提供共享参数
- WithConcurrentNum 最大并发数

### 4. 触发执行

调用Notify,传递参数:

```go
exec.Notify(params)
```

达到阈值时会异步执行execFunc。

执行前会检查并发数是否超限。

## TODO

- [ ] 提供启动/停止方法
- [ ] 增加测试用例

欢迎提出改进意见!