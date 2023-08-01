package periodic_test

import (
	"log"
	"sync/atomic"
	"testing"
	"time"

	"github.com/474420502/execute/batch/periodic"
)

func TestA1(t *testing.T) {
	var counter atomic.Int32

	e := periodic.NewExecuteInterval[int](func(i, item int) {
		counter.Add(1)
		if i == 0 {
			log.Println(0, item, counter.Load())
		}
	}).WithPeriodic(time.Second).AsyncExecute()

	for i := 0; i < 67; i++ {
		if i >= 63 {
			log.Println("sleep")
			time.Sleep(time.Second)
		}
		e.Collect(i)
	}

	log.Println("100 test")
	for i := 0; i < 100; i++ {
		e.Collect(i)
		time.Sleep(time.Millisecond * 100)
	}
}

func TestA2(t *testing.T) {
	var counter atomic.Int32

	e := periodic.NewExecuteCompensate[int](func(i, item int) {
		counter.Add(1)

		if i == 0 {
			log.Println(0, item, counter.Load())
		}
	}).WithPeriodic(time.Second).AsyncExecute()

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

func TestA3(t *testing.T) {
	var counter atomic.Int32

	e := periodic.NewConcurrentExecute[int](func(i, item int) {
		counter.Add(1)
		if i == 0 {
			log.Println(0, item, counter.Load())
		}

	}).WithPeriodic(time.Millisecond * 500).AsyncExecute()

	for i := 0; i < 30; i++ {
		time.Sleep(time.Millisecond * 100)
		e.Collect(i)
	}

	for i := 0; i < 30; i++ {
		e.Collect(i)
		time.Sleep(time.Millisecond * 100)
	}
	time.Sleep(time.Millisecond * 2000)
}

// func Benchmark(t *testing.T) {
// 	var counter atomic.Int32

// 	e := periodic.NewConcurrentExecute[int](func(i, item int) {
// 		counter.Add(1)
// 		if i == 0 {
// 			log.Println(0, item, counter.Load())
// 		}

// 	}).WithPeriodic(time.Millisecond * 500).AsyncExecute()

// 	for i := 0; i < 30; i++ {
// 		time.Sleep(time.Millisecond * 100)
// 		e.Collect(i)
// 	}

// 	for i := 0; i < 30; i++ {
// 		e.Collect(i)
// 		time.Sleep(time.Millisecond * 100)
// 	}
// 	time.Sleep(time.Millisecond * 2000)
// }
