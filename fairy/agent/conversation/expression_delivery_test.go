package conversation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fairy/agent/conversation/delivery"
	"fairy/transport/session"
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
			registry := delivery.NewRegistry(time.Second)
			key := delivery.Key{ConversationID: "conversation-1", TurnID: "turn-1", BeatID: "beat-1"}
			err := registry.Await(t.Context(), key, func() error {
				return registry.Report(test.result)
			})
			if test.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("delivery error = %v", err)
			}
			if err := registry.Report(test.result); !errors.Is(err, delivery.ErrNotPending) {
				t.Fatalf("late report error = %v", err)
			}
		})
	}
}

func TestExpressionDeliveryRegistryTimesOutAndCancels(t *testing.T) {
	registry := delivery.NewRegistry(5 * time.Millisecond)
	key := delivery.Key{ConversationID: "conversation-1", TurnID: "turn-1", BeatID: "beat-1"}
	if err := registry.Await(t.Context(), key, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := registry.Await(ctx, key, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestExpressionDeliveryRegistryBoundsPendingBeforePublish(t *testing.T) {
	registry := delivery.NewRegistryWithCapacity(time.Second, 1)
	firstKey := delivery.Key{ConversationID: "conversation-1", TurnID: "turn-1", BeatID: "beat-1"}
	secondKey := delivery.Key{ConversationID: "conversation-2", TurnID: "turn-2", BeatID: "beat-2"}
	started := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- registry.Await(context.Background(), firstKey, func() error {
			close(started)
			return nil
		})
	}()
	<-started

	var secondPublished atomic.Bool
	err := registry.Await(t.Context(), secondKey, func() error {
		secondPublished.Store(true)
		return nil
	})
	if !errors.Is(err, delivery.ErrOverloaded) {
		t.Fatalf("overflow error = %v", err)
	}
	if secondPublished.Load() {
		t.Fatal("overflow expression was published")
	}
	pending := registry.PendingCount()
	if pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}

	registry.Close()
	if err := <-firstDone; !errors.Is(err, delivery.ErrClosed) {
		t.Fatalf("pending await after Close = %v", err)
	}
}

func TestExpressionDeliveryRegistryCloseRejectsAdmissionAndRacesWithReport(t *testing.T) {
	for index := 0; index < 100; index++ {
		registry := delivery.NewRegistryWithCapacity(time.Second, 1)
		key := delivery.Key{ConversationID: "conversation", TurnID: "turn", BeatID: "beat"}
		started := make(chan struct{})
		awaitDone := make(chan error, 1)
		go func() {
			awaitDone <- registry.Await(context.Background(), key, func() error {
				close(started)
				return nil
			})
		}()
		<-started

		result := session.ExpressionDeliveryResult{
			ConversationID: key.ConversationID, TurnID: key.TurnID, BeatID: key.BeatID,
			Status: session.ExpressionDeliverySucceeded,
		}
		var concurrent sync.WaitGroup
		concurrent.Add(2)
		go func() {
			defer concurrent.Done()
			_ = registry.Report(result)
		}()
		go func() {
			defer concurrent.Done()
			registry.Close()
		}()
		concurrent.Wait()
		err := <-awaitDone
		if err != nil && !errors.Is(err, delivery.ErrClosed) {
			t.Fatalf("await result = %v", err)
		}
		pending := registry.PendingCount()
		if pending != 0 {
			t.Fatalf("pending after Close = %d", pending)
		}
		var published atomic.Bool
		if err := registry.Await(t.Context(), key, func() error {
			published.Store(true)
			return nil
		}); !errors.Is(err, delivery.ErrClosed) {
			t.Fatalf("post-close admission = %v", err)
		}
		if published.Load() {
			t.Fatal("post-close expression was published")
		}
	}
}
