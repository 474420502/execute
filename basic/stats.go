package basic

import "time"

// Stats 是执行器运行指标的只读快照.
//
// 计数均为累计值, 语义如下:
//   - ReceivedItems: 成功进入执行器缓冲区的数据条数(不含被丢弃的);
//   - ProcessedItems: 交给 Handler 处理过的数据条数;
//   - SucceededItems: 处理过程中未产生错误/panic 的数据条数;
//   - FailedItems: 因 Handler 返回 error 或 panic 而未成功的数据条数;
//   - DroppedItems: 因背压策略(丢弃模式/超出上限)而未被接收的数据条数;
//   - Executions: Handler 被调用的批次数;
//   - FailedExecutions: 其中失败的批次数.
type Stats struct {
	ReceivedItems    uint64
	ProcessedItems   uint64
	SucceededItems   uint64
	FailedItems      uint64
	DroppedItems     uint64
	Executions       uint64
	FailedExecutions uint64

	// PendingItems 是快照时刻仍在缓冲区中等待的数据条数.
	PendingItems int
	// InFlight 是快照时刻正在并发执行的批次数量.
	InFlight int64

	// LastError / LastErrorAt 记录最近一次失败的描述与时间.
	LastError   string
	LastErrorAt time.Time
	// RecentErrors 记录最近若干次失败, 便于排障.
	RecentErrors []ErrorRecord
}

// ErrorRecord 描述一次失败的执行.
type ErrorRecord struct {
	// At 是失败发生时间.
	At time.Time
	// Items 是本次失败批次的数据条数.
	Items int
	// Err 是失败描述(Handler 返回的 error 或 panic 的格式化文本).
	Err string
	// Panicked 表示失败来自 panic 而不是 error 返回值.
	Panicked bool
}

// ErrorRate 返回失败批次占已执行批次的比例, 取值 [0,1].
func (s Stats) ErrorRate() float64 {
	if s.Executions == 0 {
		return 0
	}
	return float64(s.FailedExecutions) / float64(s.Executions)
}
