package presence

import "sync"

// boundedQueue is Initiative's fixed-capacity, non-blocking FIFO. It is kept
// private because its overflow and shutdown behavior are part of Initiative's
// worker policy rather than a repository-wide abstraction.
type boundedQueue[T any] struct {
	mu     sync.Mutex
	ch     chan T
	closed bool
}

func newBoundedQueue[T any](capacity int) *boundedQueue[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &boundedQueue[T]{ch: make(chan T, capacity)}
}

func (q *boundedQueue[T]) tryPush(value T) bool {
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

func (q *boundedQueue[T]) receive() <-chan T {
	if q == nil {
		ch := make(chan T)
		close(ch)
		return ch
	}
	return q.ch
}

func (q *boundedQueue[T]) close() {
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

func (q *boundedQueue[T]) capacity() int {
	if q == nil {
		return 0
	}
	return cap(q.ch)
}
