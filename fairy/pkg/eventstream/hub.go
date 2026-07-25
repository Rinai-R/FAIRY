package eventstream

import "sync"

type subscriber[E any] struct {
	events   chan E
	failures chan error
}

// Subscription is one ordered event stream with a separate terminal failure.
type Subscription[E any] struct {
	Events      <-chan E
	Failures    <-chan error
	unsubscribe func()
}

func (s Subscription[E]) Unsubscribe() {
	if s.unsubscribe != nil {
		s.unsubscribe()
	}
}

// Hub is a bounded, non-blocking keyed fan-out. Slow subscribers are removed
// with the configured overflow error instead of blocking publishers.
type Hub[K comparable, E any] struct {
	mu          sync.Mutex
	buffer      int
	overflowErr error
	subs        map[K]map[*subscriber[E]]struct{}
	closed      bool
}

func New[K comparable, E any](buffer int, overflowErr error) *Hub[K, E] {
	if buffer <= 0 {
		panic("event stream buffer must be positive")
	}
	if overflowErr == nil {
		panic("event stream overflow error is required")
	}
	return &Hub[K, E]{buffer: buffer, overflowErr: overflowErr, subs: make(map[K]map[*subscriber[E]]struct{})}
}

func (h *Hub[K, E]) Subscribe(key K) Subscription[E] {
	if h == nil {
		return closedSubscription[E]()
	}
	sub := &subscriber[E]{events: make(chan E, h.buffer), failures: make(chan error, 1)}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(sub.events)
		close(sub.failures)
		return Subscription[E]{Events: sub.events, Failures: sub.failures, unsubscribe: func() {}}
	}
	if h.subs[key] == nil {
		h.subs[key] = make(map[*subscriber[E]]struct{})
	}
	h.subs[key][sub] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	return Subscription[E]{Events: sub.events, Failures: sub.failures, unsubscribe: func() {
		once.Do(func() {
			h.mu.Lock()
			h.removeLocked(key, sub, nil)
			h.mu.Unlock()
		})
	}}
}

func closedSubscription[E any]() Subscription[E] {
	events := make(chan E)
	failures := make(chan error)
	close(events)
	close(failures)
	return Subscription[E]{Events: events, Failures: failures, unsubscribe: func() {}}
}

func (h *Hub[K, E]) Publish(key K, event E) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs[key] {
		select {
		case sub.events <- event:
		default:
			h.removeLocked(key, sub, h.overflowErr)
		}
	}
}

func (h *Hub[K, E]) removeLocked(key K, sub *subscriber[E], failure error) {
	subscribers, ok := h.subs[key]
	if !ok {
		return
	}
	if _, ok := subscribers[sub]; !ok {
		return
	}
	delete(subscribers, sub)
	if len(subscribers) == 0 {
		delete(h.subs, key)
	}
	if failure != nil {
		sub.failures <- failure
	}
	close(sub.failures)
	close(sub.events)
}

func (h *Hub[K, E]) SubscriberCount() uint64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var count uint64
	for _, subscribers := range h.subs {
		count += uint64(len(subscribers))
	}
	return count
}

func (h *Hub[K, E]) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for key, subscribers := range h.subs {
		for sub := range subscribers {
			h.removeLocked(key, sub, nil)
		}
	}
}
