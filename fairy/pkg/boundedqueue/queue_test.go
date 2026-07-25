package boundedqueue

import (
	"sync/atomic"
	"testing"
)

func TestTryPushDropsWhenFullAndAfterClose(t *testing.T) {
	q := New[int](1)
	if !q.TryPush(1) {
		t.Fatal("first push should succeed")
	}
	if q.TryPush(2) {
		t.Fatal("full queue should drop")
	}
	if got := <-q.Recv(); got != 1 {
		t.Fatalf("recv = %d", got)
	}
	if !q.TryPush(3) {
		t.Fatal("push after drain should succeed")
	}
	q.Close()
	if q.TryPush(4) {
		t.Fatal("closed queue should reject")
	}
	if got := <-q.Recv(); got != 3 {
		t.Fatalf("drained closed value = %d", got)
	}
	if _, ok := <-q.Recv(); ok {
		t.Fatal("closed queue should finish")
	}
}

func TestTryPushConcurrentNoPanic(t *testing.T) {
	q := New[int](8)
	var dropped atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range q.Recv() {
		}
	}()
	for i := 0; i < 1000; i++ {
		if !q.TryPush(i) {
			dropped.Add(1)
		}
	}
	q.Close()
	<-done
	if dropped.Load() < 0 {
		t.Fatal("dropped counter invalid")
	}
}
