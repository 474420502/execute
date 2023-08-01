package threshold_test

import (
	"log"
	"sync/atomic"
	"testing"
	"time"

	"github.com/474420502/execute/batch/threshold"
)

func TestA1(t *testing.T) {
	var counter atomic.Int32

	e := threshold.NewThresholdExecute[int](func(i int, item int) {
		counter.Add(int32(item))
		log.Println(item)
	}).WithBatchSize(64).WithPeriodic(time.Second).AsyncExecute()

	for i := 0; i < 67; i++ {
		if i >= 63 {
			log.Println("sleep")
			time.Sleep(time.Second)
		}
		e.Collect(i)
	}

	for i := 0; i < 100; i++ {
		e.Collect(i)
		time.Sleep(time.Millisecond * 100)
	}

}
