package triggered

import (
	"log"
	"sync/atomic"
	"testing"
	"time"
)

var be = NewEventTriggeredExecute[int]()

func TestA1(t *testing.T) {

	b := atomic.Int64{}

	var CountBoy Event = be.RegisterExecute(func(params *Params[int]) {
		b.Add(1)
		time.Sleep(time.Second)
	}, nil)

	for i := 0; i < 30; i++ {
		be.Notify(CountBoy, nil)
		time.Sleep(time.Millisecond * 100)
	}

	if b.Load() > 3 {
		t.Error("????")
	}

	log.Println(b.Load())
}
