package basic

// Status 描述执行器的生命周期状态.
type Status int32

const (
	// StatusCreated 已创建, 尚未启动(启动是惰性的, 首次收集或显式 Start 时进入 Running).
	StatusCreated Status = iota
	// StatusRunning 正在运行, 可以接收数据并执行.
	StatusRunning
	// StatusPaused 已暂停: 仍然接收数据, 但不会触发执行.
	StatusPaused
	// StatusClosing 正在关闭: 不再接收新数据, 等待在途任务结束.
	StatusClosing
	// StatusStopped 已停止, 所有资源已释放.
	StatusStopped
)

func (s Status) String() string {
	switch s {
	case StatusCreated:
		return "created"
	case StatusRunning:
		return "running"
	case StatusPaused:
		return "paused"
	case StatusClosing:
		return "closing"
	case StatusStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// Runnable 返回是否处于可接收数据的状态.
func (s Status) Runnable() bool {
	return s == StatusCreated || s == StatusRunning || s == StatusPaused
}

// Terminal 返回是否处于终态(不会再执行任何任务).
func (s Status) Terminal() bool {
	return s == StatusClosing || s == StatusStopped
}
