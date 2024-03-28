package utils

import (
	"sync/atomic"
)

type OnceNoWait struct {
	done atomic.Int32
}

// Do 实现类似于 sync.Once.Do 的功能
func (o *OnceNoWait) Do(do func()) bool {
	if o.done.CompareAndSwap(undone, done) {
		do() // 执行函数
		return true
	}
	return false

}

// Reset 重置可以重新使用Once的Do
func (o *OnceNoWait) Reset() {
	o.done.Store(undone)
}
