package basic

import (
	"context"
	"sync"
	"sync/atomic"
)

// Semaphore 是一个可动态调整上限的计数信号量, 用于限制并发度.
//
// 三个概念要区分清楚:
//
//	limit —— 当前并发上限, 可通过 Resize 在运行期调整;
//	token —— 目前可用的 permit 数(缓冲通道内的元素个数);
//	inUse —— limit - token, 即在途持有者数量。
//
// 不变式: 0 <= token <= limit <= cap(slots)。cap(slots) 是创建时申请的
// "允许扩容到的最大并发度", limit 则是运行期实际生效的上限。
//
// 所有方法都可并发调用.
type Semaphore struct {
	slots chan struct{}
	limit atomic.Int64

	// mu 串行化"判定 + 取用", 保证任何时刻都不会超发 permit.
	mu sync.Mutex
}

// NewSemaphore 创建信号量; n < 1 时按 1 处理.
//
// 通道容量即允许扩容到的最大并发度, 初始 limit 为 n.
func NewSemaphore(n int) *Semaphore {
	if n < 1 {
		n = 1
	}
	s := &Semaphore{slots: make(chan struct{}, n)}
	s.limit.Store(int64(n))
	for range n {
		s.slots <- struct{}{}
	}
	return s
}

// Cap 返回允许扩容到的最大并发度.
func (s *Semaphore) Cap() int { return cap(s.slots) }

// Limit 返回当前并发上限.
func (s *Semaphore) Limit() int { return int(s.limit.Load()) }

// Available 返回当前可用的 permit 数.
func (s *Semaphore) Available() int { return len(s.slots) }

// InUse 返回当前在途(已获取未归还)的 permit 数.
func (s *Semaphore) InUse() int { return s.Limit() - len(s.slots) }

// TryAcquire 非阻塞地获取一个 permit; 成功时调用方必须稍后 Release.
func (s *Semaphore) TryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.slots:
		return true
	default:
		return false
	}
}

// Acquire 阻塞获取一个 permit; ctx 用于取消等待.
//
// 返回值:
//   - (true, nil):  已获得 permit, 调用方必须 Release;
//   - (false, err): 未获得, err 为 ctx 的错误.
func (s *Semaphore) Acquire(ctx context.Context) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// 快路径: 有空闲 permit 时立即获取, 避免进入 select.
	s.mu.Lock()
	select {
	case <-s.slots:
		s.mu.Unlock()
		return true, nil
	default:
	}
	s.mu.Unlock()

	select {
	case <-s.slots:
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// AcquireWait 无限期等待空闲 permit.
func (s *Semaphore) AcquireWait() bool {
	ok, _ := s.Acquire(context.Background())
	return ok
}

// Resize 在运行期调整并发上限.
//
// 规则:
//   - 目标上限会被截断到通道容量;
//   - 不剥夺在途持有者的额度: 若在途数已超过新上限, 只更新 limit,
//     多余的额度会在 Release 时自然消失;
//   - 调整后保证 可用 permit == limit - 在途数, 因此不会超发。
//
// 返回调整后的并发上限.
func (s *Semaphore) Resize(n int) int {
	if n < 1 {
		n = 1
	}
	if n > s.Cap() {
		n = s.Cap()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	inUse := s.InUse() // 必须在修改 limit 之前计算
	s.limit.Store(int64(n))

	// 先清空空闲 token, 再按新的 limit 补足.
drain:
	for {
		select {
		case <-s.slots:
		default:
			break drain
		}
	}
	want := n - inUse
	if want < 0 {
		want = 0 // 在途超出新上限: 不补 token, 等 Release 归还
	}
	for range want {
		select {
		case s.slots <- struct{}{}:
		default:
			return s.Limit()
		}
	}
	return s.Limit()
}

// Release 归还一个 permit; 超过当前上限的归还会被丢弃.
func (s *Semaphore) Release() {
	select {
	case s.slots <- struct{}{}:
	default:
		// 容量已满(例如已被缩容): 多余的归还没有意义, 直接丢弃.
	}
}
