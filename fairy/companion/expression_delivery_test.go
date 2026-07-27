package companion

import (
	"context"
	"errors"
	"strings"
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
