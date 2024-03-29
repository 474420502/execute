package periodic_test

import (
	"log"
	"sync/atomic"
	"testing"
	"time"

	"github.com/474420502/execute/batch/periodic"
)

func TestA2(t *testing.T) {
	var counter atomic.Int32

	e := periodic.NewExecuteCompensate[int](func(item int) {
		counter.Add(1)
		if counter.Load() == 1 {
			log.Println(0, item, counter.Load())
		}
	}).WithPeriodic(time.Second)

	for i := 0; i < 67; i++ {
		if i >= 63 {
			log.Println("sleep")
			time.Sleep(time.Millisecond * 500)
		}
		e.Collect(i)
	}

	for i := 0; i < 100; i++ {
		e.Collect(i)
		time.Sleep(time.Millisecond * 100)
	}
}
