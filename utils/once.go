package utils

import (
	"sync"
	"sync/atomic"
)

const (
	onceUndone int32 = iota
	onceDone
)

// Once 是 sync.Once 的增强版本: 除了保证函数只执行一次以外,
// 还通过返回值告诉调用方"这一次执行是否由我触发".
//
// 与 sync.Once 不同, 它额外提供 Reset 以便复用.
// 语义保证:
//   - 第一个调用 Do 的 goroutine 执行 do 并返回 true;
//   - 在此之后所有调用方都返回 false, 且能看到 do 产生的内存写入
//     (Do 返回时存在 happens-before 关系);
//   - 当 do panic 时, 标记不会置位, 下一次调用会重新尝试执行.
type Once struct {
	state atomic.Int32
	mu    sync.Mutex
}

// Do 执行 do 最多一次. 返回 true 表示本次调用真正执行了 do.
func (o *Once) Do(do func()) bool {
	if do == nil {
		return false
	}
	if o.state.Load() == onceDone { // 先快速检查是否已完成
		return false
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if o.state.Load() == onceDone { // 双重检查锁定
		return false
	}
	do()
	o.state.Store(onceDone)
	return true
}

// Done 返回是否已经执行过.
func (o *Once) Done() bool {
	return o.state.Load() == onceDone
}

// Reset 重置状态, 使得 Do 可以再次执行.
// 注意: 与 Reset 并发的 Do 调用语义等价于"重新开始一轮", 不应依赖其顺序.
func (o *Once) Reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.state.Store(onceUndone)
}

// OnceNoWait 是无锁(基于原子操作)的一次性执行器.
// 适用于"只允许一个赢家, 失败/重复调用无需等待"的场景, 例如资源注销、信号只发一次.
//
// 语义与 Once 的差异:
//   - 非阻塞: CAS 失败立即返回 false, 不会等待赢家执行完成;
//   - 因此调用方不能依赖"返回 false 时 do 已经执行完毕"的可见性保证.
type OnceNoWait struct {
	state atomic.Int32
}

// Do 尝试抢占执行权. 返回 true 表示由本次调用执行 do.
func (o *OnceNoWait) Do(do func()) bool {
	if do == nil {
		return false
	}
	if o.state.CompareAndSwap(onceUndone, onceDone) {
		do()
		return true
	}
	return false
}

// Done 返回是否已被抢占.
func (o *OnceNoWait) Done() bool {
	return o.state.Load() == onceDone
}

// Reset 重置以便重新使用.
func (o *OnceNoWait) Reset() {
	o.state.Store(onceUndone)
}
