package companion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"fairy/session"
)

const (
	expressionDeliveryTimeout  = 20 * time.Second
	expressionDeliveryCapacity = 256
)

var (
	errExpressionDeliveryNotPending = errors.New("expression delivery is not pending")
	errExpressionDeliveryDuplicate  = errors.New("expression delivery result was already reported")
	errExpressionDeliveryOverloaded = errors.New("expression delivery capacity exhausted")
	errExpressionDeliveryClosed     = errors.New("expression delivery registry is closed")
)

type expressionDeliveryKey struct {
	conversationID string
	turnID         string
	beatID         string
}

type expressionDeliveryRegistry struct {
	mu       sync.Mutex
	timeout  time.Duration
	capacity int
	closed   bool
	pending  map[expressionDeliveryKey]chan session.ExpressionDeliveryResult
}

func newExpressionDeliveryRegistry(timeout time.Duration) *expressionDeliveryRegistry {
	return newExpressionDeliveryRegistryWithCapacity(timeout, expressionDeliveryCapacity)
}

func newExpressionDeliveryRegistryWithCapacity(timeout time.Duration, capacity int) *expressionDeliveryRegistry {
	if timeout <= 0 {
		timeout = expressionDeliveryTimeout
	}
	if capacity < 1 {
		capacity = 1
	}
	return &expressionDeliveryRegistry{
		timeout:  timeout,
		capacity: capacity,
		pending:  make(map[expressionDeliveryKey]chan session.ExpressionDeliveryResult),
	}
}

func (registry *expressionDeliveryRegistry) await(
	ctx context.Context,
	key expressionDeliveryKey,
	publish func() error,
) error {
	if registry == nil {
		return errors.New("expression delivery registry is unavailable")
	}
	registry.mu.Lock()
	if registry.closed {
		registry.mu.Unlock()
		return errExpressionDeliveryClosed
	}
	if _, exists := registry.pending[key]; exists {
		registry.mu.Unlock()
		return errors.New("expression delivery is already pending")
	}
	if len(registry.pending) >= registry.capacity {
		registry.mu.Unlock()
		return errExpressionDeliveryOverloaded
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
			return errExpressionDeliveryClosed
		}
		if delivered.Status == session.ExpressionDeliveryFailed {
			return fmt.Errorf("surface sticker delivery failed: %s", delivered.ErrorMessage)
		}
		return nil
	}
}

func (registry *expressionDeliveryRegistry) report(result session.ExpressionDeliveryResult) error {
	if registry == nil {
		return errors.New("expression delivery registry is unavailable")
	}
	if err := result.Validate(); err != nil {
		return err
	}
	key := expressionDeliveryKey{
		conversationID: result.ConversationID,
		turnID:         result.TurnID,
		beatID:         result.BeatID,
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.closed {
		return errExpressionDeliveryClosed
	}
	pending := registry.pending[key]
	if pending == nil {
		return errExpressionDeliveryNotPending
	}
	select {
	case pending <- result:
		return nil
	default:
		return errExpressionDeliveryDuplicate
	}
}

func (registry *expressionDeliveryRegistry) close() {
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

func (s *CompanionService) ReportExpressionDelivery(result session.ExpressionDeliveryResult) error {
	if s == nil {
		return ErrTurnRuntimeUnavailable
	}
	return s.expressionDeliveries.report(result)
}
