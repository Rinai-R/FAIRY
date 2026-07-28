package companion

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fairy/session"
)

func TestExpressionDeliveryRegistryPropagatesSurfaceResult(t *testing.T) {
	for _, test := range []struct {
		name    string
		result  session.ExpressionDeliveryResult
		wantErr string
	}{
		{
			name: "succeeded",
			result: session.ExpressionDeliveryResult{
				ConversationID: "conversation-1", TurnID: "turn-1", BeatID: "beat-1",
				Status: session.ExpressionDeliverySucceeded,
			},
		},
		{
			name: "failed",
			result: session.ExpressionDeliveryResult{
				ConversationID: "conversation-1", TurnID: "turn-1", BeatID: "beat-1",
				Status: session.ExpressionDeliveryFailed, ErrorMessage: "OneBot rejected image",
			},
			wantErr: "OneBot rejected image",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := newExpressionDeliveryRegistry(time.Second)
			key := expressionDeliveryKey{conversationID: "conversation-1", turnID: "turn-1", beatID: "beat-1"}
			err := registry.await(t.Context(), key, func() error {
				return registry.report(test.result)
			})
			if test.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("delivery error = %v", err)
			}
			if err := registry.report(test.result); !errors.Is(err, errExpressionDeliveryNotPending) {
				t.Fatalf("late report error = %v", err)
			}
		})
	}
}

func TestExpressionDeliveryRegistryTimesOutAndCancels(t *testing.T) {
	registry := newExpressionDeliveryRegistry(5 * time.Millisecond)
	key := expressionDeliveryKey{conversationID: "conversation-1", turnID: "turn-1", beatID: "beat-1"}
	if err := registry.await(t.Context(), key, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := registry.await(ctx, key, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestExpressionDeliveryRegistryBoundsPendingBeforePublish(t *testing.T) {
	registry := newExpressionDeliveryRegistryWithCapacity(time.Second, 1)
	firstKey := expressionDeliveryKey{conversationID: "conversation-1", turnID: "turn-1", beatID: "beat-1"}
	secondKey := expressionDeliveryKey{conversationID: "conversation-2", turnID: "turn-2", beatID: "beat-2"}
	started := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- registry.await(context.Background(), firstKey, func() error {
			close(started)
			return nil
		})
	}()
	<-started

	var secondPublished atomic.Bool
	err := registry.await(t.Context(), secondKey, func() error {
		secondPublished.Store(true)
		return nil
	})
	if !errors.Is(err, errExpressionDeliveryOverloaded) {
		t.Fatalf("overflow error = %v", err)
	}
	if secondPublished.Load() {
		t.Fatal("overflow expression was published")
	}
	registry.mu.Lock()
	pending := len(registry.pending)
	registry.mu.Unlock()
	if pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}

	registry.close()
	if err := <-firstDone; !errors.Is(err, errExpressionDeliveryClosed) {
		t.Fatalf("pending await after Close = %v", err)
	}
}

func TestExpressionDeliveryRegistryCloseRejectsAdmissionAndRacesWithReport(t *testing.T) {
	for index := 0; index < 100; index++ {
		registry := newExpressionDeliveryRegistryWithCapacity(time.Second, 1)
		key := expressionDeliveryKey{conversationID: "conversation", turnID: "turn", beatID: "beat"}
		started := make(chan struct{})
		awaitDone := make(chan error, 1)
		go func() {
			awaitDone <- registry.await(context.Background(), key, func() error {
				close(started)
				return nil
			})
		}()
		<-started

		result := session.ExpressionDeliveryResult{
			ConversationID: key.conversationID, TurnID: key.turnID, BeatID: key.beatID,
			Status: session.ExpressionDeliverySucceeded,
		}
		var concurrent sync.WaitGroup
		concurrent.Add(2)
		go func() {
			defer concurrent.Done()
			_ = registry.report(result)
		}()
		go func() {
			defer concurrent.Done()
			registry.close()
		}()
		concurrent.Wait()
		err := <-awaitDone
		if err != nil && !errors.Is(err, errExpressionDeliveryClosed) {
			t.Fatalf("await result = %v", err)
		}
		registry.mu.Lock()
		pending := len(registry.pending)
		registry.mu.Unlock()
		if pending != 0 {
			t.Fatalf("pending after Close = %d", pending)
		}
		var published atomic.Bool
		if err := registry.await(t.Context(), key, func() error {
			published.Store(true)
			return nil
		}); !errors.Is(err, errExpressionDeliveryClosed) {
			t.Fatalf("post-close admission = %v", err)
		}
		if published.Load() {
			t.Fatal("post-close expression was published")
		}
	}
}
