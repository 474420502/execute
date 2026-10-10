package engine

import (
	"sync"
	"time"
)

// tickMode 决定节拍落后时的补偿策略.
type tickMode uint8

const (
	// tickSkip 落后时跳过已经错过的网格点(与 time.Ticker 一致, 会丢拍).
	tickSkip tickMode = iota
	// tickCompensate 落后时把网格点推进到"最近一个仍未到期的网格点",
	// 使下一次触发尽可能立即发生, 从而保证长期平均速率 = 1/周期.
	tickCompensate
)

// delayQueue 是一个可重配置的单消费者"定时网格".
//
// 语义:
//   - 网格点按 p+d 顺延, 因而长期平均频率稳定在 1/d;
//   - 发送策略是"先排空、再投递最新节拍": 通道容量为 1, 旧的待消费节拍会被丢弃,
//     因此消费者拿到的永远是最新的节拍, 不会因为排队而处理过期节拍;
//   - 消费不及时也不会积压/阻塞生产者, 最多占用 1 个缓冲位;
//   - tickSkip 模式落后时跳过错过的网格点(丢拍不连发);
//   - tickCompensate 模式落后时把网格推进到最近的未到期网格点, 让消费者紧接着执行,
//     用于"批次耗时可能超过周期"的固定速率补偿场景;
//   - Set 修改周期后立即以"当前时刻"为新网格原点重新计时.
type delayQueue struct {
	mu    sync.Mutex
	next  time.Time // 下一个网格点(即定时器的触发时刻)
	d     time.Duration
	mode  tickMode
	v     uint64 // 配置版本, 使旧定时器回调失效
	timer *time.Timer
	// ch 只作为"有新的节拍"的唤醒信号, 不承载数据.
	ch chan struct{}
	// lastFired 是最近一次已投递的节拍时刻(补偿模式下即心跳时刻);
	// 由 fire 写入、消费者读取, 保证消费者永远拿到最新值而不是排队中的旧值.
	lastFired time.Time
	stopped   bool
}

func newDelayQueue(mode tickMode) *delayQueue {
	return &delayQueue{ch: make(chan struct{}, 1), mode: mode}
}

// Set 更新周期; d <= 0 表示停止节拍.
//
// 网格原点策略:
//   - 首次调用或周期发生变化: 以 now+d 为新的第一个网格点, 保证"改完立即按新周期计时";
//   - 周期未变化: 保持当前网格点, 避免无谓地推迟下一次触发.
func (q *delayQueue) Set(d time.Duration) {
	q.mu.Lock()
	defer q.mu.Unlock()

	changed := q.d != d
	q.d = d
	q.v++
	if q.timer != nil {
		q.timer.Stop()
		q.timer = nil
	}
	if q.stopped || d <= 0 {
		return
	}

	now := time.Now()
	if !changed && q.next.After(now) {
		q.armLocked(q.v) // 周期没变: 沿用原网格点
		return
	}
	q.next = now.Add(d)
	q.armLocked(q.v)
}

func (q *delayQueue) armLocked(version uint64) {
	delay := time.Until(q.next)
	if delay < 0 {
		delay = 0
	}
	at := q.next
	q.timer = time.AfterFunc(delay, func() { q.fire(version, at) })
}

// advance 计算下一个网格点.
//
//	compensate 为 true 时采用"稳定心跳": 定时器始终按固定间隔触发,
//	是否真正执行由消费者依据自己的截止时间决定 —— 这样即使消费者很慢,
//	心跳也不会被跳过, 从而保证长期平均速率不受处理耗时影响;
//	为 false 时跳过所有已经错过的网格点(与 time.Ticker 一致).
func (q *delayQueue) advance(next time.Time, compensate bool) time.Time {
	now := time.Now()
	if compensate {
		return now.Add(q.d)
	}
	for !next.After(now) {
		next = next.Add(q.d)
	}
	return next
}

func (q *delayQueue) fire(version uint64, scheduled time.Time) {
	q.mu.Lock()
	if q.stopped || q.v != version {
		q.mu.Unlock()
		return
	}
	q.timer = nil
	if q.d > 0 {
		q.next = q.advance(scheduled.Add(q.d), q.mode == tickCompensate)
		q.armLocked(version)
	}
	q.mu.Unlock()

	// 发布最新节拍: 唤醒信号只保留 1 个(排空后重发), 时间值写入 lastFired,
	// 因此消费者读到的永远是新鲜的网格点, 不会处理排队中的过期节拍.
	q.mu.Lock()
	q.lastFired = scheduled
	q.mu.Unlock()

drain:
	for {
		select {
		case <-q.ch:
		default:
			break drain
		}
	}
	select {
	case q.ch <- struct{}{}:
	default:
	}
}

// LastFired 返回最近一次投递的网格点; 从未投递过时返回零值.
func (q *delayQueue) LastFired() time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.lastFired
}

// C 返回节拍唤醒通道.
func (q *delayQueue) C() <-chan struct{} { return q.ch }

// Stop 停止出队并释放定时器.
func (q *delayQueue) Stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopped = true
	q.v++
	if q.timer != nil {
		q.timer.Stop()
		q.timer = nil
	}
}
