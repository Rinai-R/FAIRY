package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"fairy/agent/conversation/delivery"
	"fairy/transport/session"
)

func TestCloseCancelsActiveTurn(t *testing.T) {
	s := NewService()
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
	s := NewService()
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
	s := NewService()
	s.expressionDeliveries = delivery.NewRegistryWithCapacity(time.Hour, 1)
	key := delivery.Key{ConversationID: "conversation-1", TurnID: "turn-1", BeatID: "beat-1"}
	started := make(chan struct{})
	awaitDone := make(chan error, 1)
	go func() {
		awaitDone <- s.expressionDeliveries.Await(context.Background(), key, func() error {
			close(started)
			return nil
		})
	}()
	<-started
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-awaitDone; !errors.Is(err, delivery.ErrClosed) {
		t.Fatalf("pending expression after Close = %v", err)
	}
	pending := s.expressionDeliveries.PendingCount()
	if pending != 0 {
		t.Fatalf("pending expression after Close = %d", pending)
	}
}
