package utils

import (
	"sync"
	"sync/atomic"
)

type Once struct {
	done atomic.Int32
	mu   sync.Mutex
}

const (
	undone = iota
	done
)

// Do 实现类似于 sync.Once.Do 的功能
func (o *Once) Do(do func()) bool {
	if o.done.Load() == undone { // 先快速检查是否已完成
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.done.Load() == undone { // 再次检查(锁定状态下),双重检查锁定
			do()               // 执行函数
			o.done.Store(done) // 完成后设置标记
			return true
		}
	}
	return false
}
