package bridge

import "sync"

type Dedupe struct {
	mu       sync.Mutex
	capacity int
	queue    []string
	seen     map[string]struct{}
}

func NewDedupe(capacity int) *Dedupe {
	if capacity <= 0 {
		capacity = 2048
	}
	return &Dedupe{capacity: capacity, seen: make(map[string]struct{}, capacity)}
}
func (d *Dedupe) Add(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id == "" {
		return false
	}
	if _, ok := d.seen[id]; ok {
		return false
	}
	d.seen[id] = struct{}{}
	d.queue = append(d.queue, id)
	if len(d.queue) > d.capacity {
		delete(d.seen, d.queue[0])
		d.queue = d.queue[1:]
	}
	return true
}
