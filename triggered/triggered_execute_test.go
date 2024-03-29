package triggered

import (
	"log"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/474420502/execute/utils"
)

func init() {
	log.SetFlags(log.Llongfile)
}

func TestEventExecute(t *testing.T) {
	var (
		executed       int
		receivedParams []any
		sharedVal      string
	)

	// 创建一个执行单元，其中执行函数将更新执行计数和参数记录
	exec := RegisterExecute[any](func(params *Items[any]) {
		executed++
		receivedParams = append(receivedParams, params.Value...)
	})

	// 发送多个通知
	for i := 0; i < 5; i++ {
		exec.Notify(i)
	}

	// 等待足够长的时间以便执行循环处理通知
	time.Sleep(time.Millisecond * 100)

	// 验证执行次数和接收到的参数
	if executed != 1 {
		t.Errorf("期望执行次数为1，但实际上执行了 %d 次", executed)
	}

	expectedParams := []any{0, 1, 2, 3, 4}
	if !reflect.DeepEqual(receivedParams, expectedParams) {
		t.Errorf("期望接收到的参数为 %v，但实际上收到的是 %v", expectedParams, receivedParams)
	}

	// 停止执行单元
	exec.Close()

	// 验证共享状态的设置和读取
	sharedVal = "test value"
	exec.sub.shared.SetValue(sharedVal)
	actualVal := ""
	exec.sub.shared.Value(func(v any) {
		actualVal = v.(string)
	})
	if actualVal != sharedVal {
		t.Errorf("期望共享值为 '%s'，但实际读取到 '%s'", sharedVal, actualVal)
	}
}

func TestNotify(t *testing.T) {
	var executed bool
	execFunc := func(params *Items[int]) {
		executed = true
	}

	exec := RegisterExecute(execFunc)
	exec.Notify(42)

	// 等待一段时间,确保执行函数已被调用
	time.Sleep(100 * time.Millisecond)

	if !executed {
		t.Error("Notify failed to trigger execution function")
	}
}

func TestWithShared(t *testing.T) {
	execFunc := func(params *Items[int]) {
		params.Shared.Value(func(v any) {
			if v.(string) != "test" {
				t.Errorf("Expected shared value 'test', got %v", v)
			}
		})

	}

	exec := RegisterExecute(execFunc)
	exec = exec.WithShared("test")
	exec.Notify(42)

	// 等待一段时间,确保执行函数已被调用
	time.Sleep(100 * time.Millisecond)
}

func TestMultipleNotify(t *testing.T) {
	var count int
	execFunc := func(params *Items[int]) {
		count += len(params.Value)
	}

	exec := RegisterExecute(execFunc)
	exec.Notify(1)
	exec.Notify(2)
	exec.Notify(3)

	// 等待一段时间,确保执行函数已被调用
	time.Sleep(100 * time.Millisecond)

	if count != 3 {
		t.Errorf("Expected count 6, got %d", count)
	}

	exec.Close()

	defer func() {
		if ierr := recover(); ierr != nil {
			log.Println(ierr)
		} else {
			t.Error("must recover after close()")
		}
	}()

	// 等待一段时间,确保执行函数已被调用
	time.Sleep(100 * time.Millisecond)
	exec.Notify(1)
	exec.Notify(2)
	exec.Notify(3)

	if count != 3 {
		t.Errorf("Expected count 6, got %d", count)
	}

}

func TestSetFinalizer(t *testing.T) {
	var o *utils.OnceNoWait
	func() {
		var count int
		execFunc := func(params *Items[int]) {
			count += len(params.Value)
		}

		exec := RegisterExecute(execFunc)
		exec.Notify(1)
		exec.Notify(2)
		exec.Notify(3)
		time.Sleep(100 * time.Millisecond)
		o = &exec.sub.stopOnce
		if count != 3 {
			t.Errorf("Expected count 6, got %d", count)
		}
	}()
	func() { runtime.GC() }()

	time.Sleep(time.Second * 2)
	if o.Do(func() {}) {
		t.Error("SetFinalizer Error")
	}
}
