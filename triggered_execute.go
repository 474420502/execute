package execute

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
)

// EventTriggeredExecute实现了一种基于事件触发的执行和错误处理机制
// 可以注册执行函数及恢复函数,通过事件通知触发执行
// 并且具有错误恢复和保护能力

// Event定义了事件类型,Uint32可以支持多达40亿个事件
type Event uint32

// eventExecute封装了一个执行单元
// 包含执行函数,恢复函数和相关控制参数
type eventExecute[PARAMS any] struct {

	// counter用来记录通知次数
	counter atomic.Uint64

	// 达到指定通知次数时触发执行
	notifyOverCountToExecute uint64

	// 是否打印恢复日志
	isShowLog atomic.Bool

	// 要执行的函数
	execDo func(params *PARAMS)

	// 错误恢复函数
	recoverDo func(ierr any)
}

// EventTriggeredExecute 包含已注册的执行事件列表
type EventTriggeredExecute[PARAMS any] struct {

	// 互斥锁保护events切片
	mu sync.Mutex

	// 已注册的执行事件列表
	events []*eventExecute[PARAMS]
}

// NewEventTriggeredExecute构造函数
func NewEventTriggeredExecute[PARAMS any]() *EventTriggeredExecute[PARAMS] {
	return &EventTriggeredExecute[PARAMS]{}
}

// RegisterExecute注册一个执行单元
// 返回分配的事件号
func (be *EventTriggeredExecute[PARAMS]) RegisterExecute(execDo func(params *PARAMS), recoverDo func(ierr any)) Event {

	// 加锁
	be.mu.Lock()

	// 释放锁
	defer be.mu.Unlock()

	// 构造执行单元
	ee := &eventExecute[PARAMS]{
		notifyOverCountToExecute: 1,
		execDo:                   execDo,
		recoverDo:                recoverDo,
	}

	// 添加到events列表
	be.events = append(be.events, ee)

	// 返回事件号
	return Event(len(be.events) - 1)
}

// CoverExecute用来覆盖一个已注册的执行单元
// 通过事件号定位并替换为新函数
func (be *EventTriggeredExecute[PARAMS]) CoverExecute(e Event, execDo func(params *PARAMS), recoverDo func(ierr any)) {

	// 加锁
	be.mu.Lock()

	// 释放锁
	defer be.mu.Unlock()

	// 构造新的执行单元
	ee := &eventExecute[PARAMS]{
		notifyOverCountToExecute: 1,
		execDo:                   execDo,
		recoverDo:                recoverDo,
	}

	// 通过事件号替换执行单元
	be.events[e] = ee
}

// Notify用于通知触发执行
// 根据事件号查找执行单元并检查通知次数
// 达到指定次数则触发goroutine异步执行
func (be *EventTriggeredExecute[PARAMS]) Notify(e Event, params *PARAMS) {

	// 事件号转索引
	ei := int(e)

	// 加锁
	be.mu.Lock()

	// 检查事件号合法性
	if len(be.events) < ei {
		panic(fmt.Errorf("event is not exists"))
	}

	// 获取执行单元
	exec := be.events[ei]

	// 释放锁
	be.mu.Unlock()

	// 原子加1并检查通知次数
	if exec.counter.Add(1) == exec.notifyOverCountToExecute {

		// 异步goroutine执行
		go func() {

			// 执行完成置0
			defer exec.counter.Store(0)

			// recover保护
			defer func() {
				if ierr := recover(); ierr != nil {

					// 打印错误堆栈
					if exec.isShowLog.Load() {
						log.Println(ierr)
					}

					// 调用恢复函数
					if exec.recoverDo != nil {
						exec.recoverDo(ierr)
					}
				}
			}()

			// 执行已注册函数
			exec.execDo(params)
		}()
	}
}
