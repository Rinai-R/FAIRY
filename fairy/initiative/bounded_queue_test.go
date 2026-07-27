package initiative

import (
	"sync/atomic"
	"testing"
)

func TestBoundedQueueDropsWhenFullAndAfterClose(t *testing.T) {
	queue := newBoundedQueue[int](1)
	if !queue.tryPush(1) {
		t.Fatal("first push should succeed")
	}
	if queue.tryPush(2) {
		t.Fatal("full queue should drop")
	}
	if got := <-queue.receive(); got != 1 {
		t.Fatalf("receive = %d", got)
	}
	if !queue.tryPush(3) {
		t.Fatal("push after drain should succeed")
	}
	queue.close()
	if queue.tryPush(4) {
		t.Fatal("closed queue should reject")
	}
	if got := <-queue.receive(); got != 3 {
		t.Fatalf("drained closed value = %d", got)
	}
	if _, ok := <-queue.receive(); ok {
		t.Fatal("closed queue should finish")
	}
	if queue.capacity() != 1 {
		t.Fatalf("capacity = %d", queue.capacity())
	}
}

func TestBoundedQueueConcurrentPushAndCloseDoesNotPanic(t *testing.T) {
	queue := newBoundedQueue[int](8)
	var dropped atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range queue.receive() {
		}
	}()
	for value := 0; value < 1000; value++ {
		if !queue.tryPush(value) {
			dropped.Add(1)
		}
	}
	queue.close()
	<-done
	if dropped.Load() < 0 {
		t.Fatal("dropped counter invalid")
	}
}
