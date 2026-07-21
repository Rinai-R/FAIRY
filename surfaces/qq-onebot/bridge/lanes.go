package bridge

import (
	"context"
	"errors"
	"sync"
)

var ErrLaneFull = errors.New("group lane is full")

type Job func(context.Context)
type lane struct {
	queue  chan Job
	cancel context.CancelFunc
	wg     sync.WaitGroup
}
type Lanes struct {
	mu       sync.Mutex
	capacity int
	lanes    map[string]*lane
	parent   context.Context
}

func NewLanes(parent context.Context, groups []string, capacity int) *Lanes {
	if capacity <= 0 {
		capacity = 16
	}
	l := &Lanes{capacity: capacity, lanes: make(map[string]*lane), parent: parent}
	for _, group := range groups {
		l.add(group)
	}
	return l
}
func (l *Lanes) add(group string) {
	ctx, cancel := context.WithCancel(l.parent)
	item := &lane{queue: make(chan Job, l.capacity), cancel: cancel}
	item.wg.Add(1)
	go func() {
		defer item.wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-item.queue:
				if job != nil {
					job(ctx)
				}
			}
		}
	}()
	l.lanes[group] = item
}
func (l *Lanes) Submit(group string, job Job) error {
	l.mu.Lock()
	item := l.lanes[group]
	l.mu.Unlock()
	if item == nil {
		return errors.New("group is not allowlisted")
	}
	select {
	case item.queue <- job:
		return nil
	default:
		return ErrLaneFull
	}
}
func (l *Lanes) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, item := range l.lanes {
		item.cancel()
		item.wg.Wait()
	}
}
