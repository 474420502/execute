package periodic

import "github.com/474420502/execute/basic/engine"

// Expr 是 NewFromConfig 返回的通用周期执行器.
//
// 三种模式(Interval/Compensate/Concurrent)共享同一份实现, 差异只由 Config.Mode
// 决定; 当调用方需要按配置驱动(例如从配置文件读取模式)时使用它, 其余场景建议
// 使用 NewInterval / NewCompensate / NewConcurrent, 语义更直观。
type Expr[ITEM any] struct {
	*engine.Engine[ITEM]
}
