package wasm

import "sync"

const MaxEventQueue = 256

type QueuedEvent struct {
	Sequence uint64
	Payload  []byte
}

// EventQueue is the bounded per-instance event buffer. Core polls by cursor.
// When full, the oldest event is dropped so the recent window remains available.
type EventQueue struct {
	mu    sync.Mutex
	seq   uint64
	items []QueuedEvent
}

func (q *EventQueue) Push(payload []byte) uint64 {
	if q == nil {
		return 0
	}
	copied := append([]byte(nil), payload...)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	q.items = append(q.items, QueuedEvent{Sequence: q.seq, Payload: copied})
	if len(q.items) > MaxEventQueue {
		q.items = q.items[len(q.items)-MaxEventQueue:]
	}
	return q.seq
}

func (q *EventQueue) Poll(after uint64, limit int) []QueuedEvent {
	if q == nil || limit <= 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]QueuedEvent, 0, min(limit, len(q.items)))
	for _, item := range q.items {
		if item.Sequence <= after {
			continue
		}
		out = append(out, QueuedEvent{Sequence: item.Sequence, Payload: append([]byte(nil), item.Payload...)})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (q *EventQueue) Depth() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}
