package companion

import (
	"context"
	"errors"
	"testing"
	"time"

	"fairy/session"
)

func TestCloseCancelsActiveTurn(t *testing.T) {
	s := NewCompanionService()
	ctx, err := s.reserveTurn("conversation-1")
	if err != nil {
		t.Fatalf("reserveTurn() error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("Close() did not cancel the active turn context")
	}
	if _, err := s.reserveTurn("conversation-1"); err != nil {
		t.Fatalf("reserveTurn() after Close() = %v, want available slot", err)
	}
}

func TestCloseIdempotentAndSafeWithoutRuntime(t *testing.T) {
	s := NewCompanionService()
	if _, err := s.reserveTurn("conversation-1"); err != nil {
		t.Fatalf("reserveTurn() error = %v", err)
	}
	if err := s.BindInteraction("conversation-1", session.Binding{
		Endpoint: session.EndpointIM,
		Facts: session.Facts{
			Audience:     session.AudienceMulti,
			Initiation:   session.InitiationAmbient,
			Presentation: session.PresentationChat,
		},
	}); err != nil {
		t.Fatalf("BindInteraction() error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if got := s.interactions.Len(); got != 0 {
		t.Fatalf("interaction cache length after Close = %d, want 0", got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestCloseWakesPendingExpressionDelivery(t *testing.T) {
	s := NewCompanionService()
	s.expressionDeliveries = newExpressionDeliveryRegistryWithCapacity(time.Hour, 1)
	key := expressionDeliveryKey{conversationID: "conversation-1", turnID: "turn-1", beatID: "beat-1"}
	started := make(chan struct{})
	awaitDone := make(chan error, 1)
	go func() {
		awaitDone <- s.expressionDeliveries.await(context.Background(), key, func() error {
			close(started)
			return nil
		})
	}()
	<-started
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-awaitDone; !errors.Is(err, errExpressionDeliveryClosed) {
		t.Fatalf("pending expression after Close = %v", err)
	}
	s.expressionDeliveries.mu.Lock()
	pending := len(s.expressionDeliveries.pending)
	s.expressionDeliveries.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending expression after Close = %d", pending)
	}
}
