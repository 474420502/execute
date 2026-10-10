package basic

// Handler 是执行器真正执行的业务逻辑. items 是本次批次的数据.
//
// 约定:
//   - items 由执行器内部复用, 仅在本次调用内有效; 如果需要在调用返回后继续持有,
//     必须自行复制(例如 slices.Clone);
//   - 返回 error 表示本批次处理失败, 执行器会记录指标并继续运行, 不会中断执行循环;
//   - panic 会被执行器捕获并按失败处理, 执行循环不会因此退出.
//
// 之所以定义为命名类型(而非匿名函数类型), 是为了让执行器内部结构体能够以
// "已实例化的类型"保存回调, 从而不必在方法上重复声明类型参数.
// 匿名函数可直接赋值或转换:
//
//	var h basic.Handler[Item] = func(items []Item) error { return nil }
type Handler[ITEM any] func(items []ITEM) error

// RecoverFunc 在 Handler panic 时被调用, 用于日志、告警、上报等副作用.
// ierr 即 recover() 的返回值. 执行器自身仍会记录失败指标, 因此
// RecoverFunc 只应做轻量、不可再 panic 的处理.
type RecoverFunc func(ierr any)
