package companion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"fairy/session"
)

const expressionDeliveryTimeout = 20 * time.Second

var (
	errExpressionDeliveryNotPending = errors.New("expression delivery is not pending")
	errExpressionDeliveryDuplicate  = errors.New("expression delivery result was already reported")
)

type expressionDeliveryKey struct {
	conversationID string
	turnID         string
	beatID         string
}

type expressionDeliveryRegistry struct {
	mu      sync.Mutex
	timeout time.Duration
	pending map[expressionDeliveryKey]chan session.ExpressionDeliveryResult
}

func newExpressionDeliveryRegistry(timeout time.Duration) *expressionDeliveryRegistry {
	if timeout <= 0 {
		timeout = expressionDeliveryTimeout
	}
	return &expressionDeliveryRegistry{
		timeout: timeout,
		pending: make(map[expressionDeliveryKey]chan session.ExpressionDeliveryResult),
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
	result := make(chan session.ExpressionDeliveryResult, 1)
	registry.mu.Lock()
	if _, exists := registry.pending[key]; exists {
		registry.mu.Unlock()
		return errors.New("expression delivery is already pending")
	}
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
	case delivered := <-result:
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
	pending := registry.pending[key]
	registry.mu.Unlock()
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

func (s *CompanionService) ReportExpressionDelivery(result session.ExpressionDeliveryResult) error {
	if s == nil {
		return ErrRespondRuntimeNotMigrated
	}
	return s.expressionDeliveries.report(result)
}
