package boundedqueue

import "sync"

// Queue is a fixed-capacity, non-blocking FIFO. Full or closed TryPush calls
// return false without waiting.
type Queue[T any] struct {
	mu     sync.Mutex
	ch     chan T
	closed bool
}

func New[T any](capacity int) *Queue[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue[T]{ch: make(chan T, capacity)}
}

// TryPush enqueues value when capacity remains. It never blocks.
func (q *Queue[T]) TryPush(value T) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	select {
	case q.ch <- value:
		return true
	default:
		return false
	}
}

// Recv returns the receive-only channel for select loops.
func (q *Queue[T]) Recv() <-chan T {
	if q == nil {
		ch := make(chan T)
		close(ch)
		return ch
	}
	return q.ch
}

// Close rejects future pushes and closes the receive channel.
func (q *Queue[T]) Close() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	close(q.ch)
}

// Cap returns the configured buffer capacity.
func (q *Queue[T]) Cap() int {
	if q == nil {
		return 0
	}
	return cap(q.ch)
}
