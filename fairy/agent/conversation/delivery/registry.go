package delivery

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"fairy/transport/session"
)

const (
	DefaultTimeout  = 20 * time.Second
	DefaultCapacity = 256
)

var (
	ErrNotPending = errors.New("expression delivery is not pending")
	ErrDuplicate  = errors.New("expression delivery result was already reported")
	ErrOverloaded = errors.New("expression delivery capacity exhausted")
	ErrClosed     = errors.New("expression delivery registry is closed")
)

type Key struct {
	ConversationID string
	TurnID         string
	BeatID         string
}

type Registry struct {
	mu       sync.Mutex
	timeout  time.Duration
	capacity int
	closed   bool
	pending  map[Key]chan session.ExpressionDeliveryResult
}

func NewRegistry(timeout time.Duration) *Registry {
	return NewRegistryWithCapacity(timeout, DefaultCapacity)
}

func NewRegistryWithCapacity(timeout time.Duration, capacity int) *Registry {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if capacity < 1 {
		capacity = 1
	}
	return &Registry{
		timeout:  timeout,
		capacity: capacity,
		pending:  make(map[Key]chan session.ExpressionDeliveryResult),
	}
}

func (registry *Registry) Await(ctx context.Context, key Key, publish func() error) error {
	if registry == nil {
		return errors.New("expression delivery registry is unavailable")
	}
	registry.mu.Lock()
	if registry.closed {
		registry.mu.Unlock()
		return ErrClosed
	}
	if _, exists := registry.pending[key]; exists {
		registry.mu.Unlock()
		return errors.New("expression delivery is already pending")
	}
	if len(registry.pending) >= registry.capacity {
		registry.mu.Unlock()
		return ErrOverloaded
	}
	result := make(chan session.ExpressionDeliveryResult, 1)
	registry.pending[key] = result
	registry.mu.Unlock()
	defer func() {
		registry.mu.Lock()
		delete(registry.pending, key)
		registry.mu.Unlock()
	}()

	if err := publish(); err != nil {
		return err
	}
	timer := time.NewTimer(registry.timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("surface sticker delivery timed out")
	case delivered, open := <-result:
		if !open {
			return ErrClosed
		}
		if delivered.Status == session.ExpressionDeliveryFailed {
			return fmt.Errorf("surface sticker delivery failed: %s", delivered.ErrorMessage)
		}
		return nil
	}
}

func (registry *Registry) Report(result session.ExpressionDeliveryResult) error {
	if registry == nil {
		return errors.New("expression delivery registry is unavailable")
	}
	if err := result.Validate(); err != nil {
		return err
	}
	key := Key{ConversationID: result.ConversationID, TurnID: result.TurnID, BeatID: result.BeatID}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closed {
		return ErrClosed
	}
	pending := registry.pending[key]
	if pending == nil {
		return ErrNotPending
	}
	select {
	case pending <- result:
		return nil
	default:
		return ErrDuplicate
	}
}

func (registry *Registry) Close() {
	if registry == nil {
		return
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closed {
		return
	}
	registry.closed = true
	for key, pending := range registry.pending {
		close(pending)
		delete(registry.pending, key)
	}
}

func (registry *Registry) PendingCount() int {
	if registry == nil {
		return 0
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return len(registry.pending)
}
