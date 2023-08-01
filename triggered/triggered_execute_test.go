package triggered

import (
	"log"
	"sync/atomic"
	"testing"
	"time"
)

func TestA1(t *testing.T) {

	b := atomic.Int64{}

	var CountBoy = RegisterExecute[int](func(params *Params[int]) {
		b.Add(1)
		time.Sleep(time.Second)
	}, nil)

	for i := 0; i < 30; i++ {
		CountBoy.Notify(nil)
		time.Sleep(time.Millisecond * 100)
	}

	if b.Load() > 3 {
		t.Error("????")
	}

	log.Println(b.Load())
}

func TestA2(t *testing.T) {
	b := atomic.Int64{}

	var CountBoy = RegisterExecute[int](func(params *Params[int]) {
		b.Add(1)
		time.Sleep(time.Second)
	}, nil)

	CountBoy.SetConcurrentNum(2)

	for i := 0; i < 30; i++ {
		CountBoy.Notify(nil)
		time.Sleep(time.Millisecond * 100)
	}

	if b.Load() > 6 {
		t.Error("????")
	}

	log.Println(b.Load())
}
