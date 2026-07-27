package companion

import (
	"context"
	"errors"
	"testing"
)

func TestCancelTurnBeforeDeliveryHonorsDeliveryBoundary(t *testing.T) {
	service := NewCompanionService()
	defer service.Close()

	ctx, err := service.reserveTurn("conversation")
	if err != nil {
		t.Fatalf("reserveTurn() error = %v", err)
	}
	service.bindTurn("conversation", "turn-1")
	if !service.cancelTurnBeforeDelivery("conversation") {
		t.Fatal("planning turn was not canceled")
	}
	if !contextCanceled(ctx) {
		t.Fatal("planning context remains active")
	}
	service.endTurn("conversation", "turn-1")

	ctx, err = service.reserveTurn("conversation")
	if err != nil {
		t.Fatalf("reserveTurn() second error = %v", err)
	}
	service.bindTurn("conversation", "turn-2")
	service.markTurnDelivering("conversation", "turn-2")
	if service.cancelTurnBeforeDelivery("conversation") {
		t.Fatal("final delivery turn was canceled by ambient input")
	}
	if contextCanceled(ctx) {
		t.Fatal("final delivery context was canceled")
	}
	service.endTurn("conversation", "turn-2")
}

func contextCanceled(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func TestMapModelCancelErrorUsesAuthoritativeTurnContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	wrappedWithoutCause := errors.New("provider returned context canceled")
	if err := mapModelCancelError(ctx, wrappedWithoutCause); !errors.Is(err, ErrTurnInterrupted) {
		t.Fatalf("mapModelCancelError() = %v, want ErrTurnInterrupted", err)
	}

	active := context.Background()
	providerErr := errors.New("provider unavailable")
	if err := mapModelCancelError(active, providerErr); !errors.Is(err, providerErr) {
		t.Fatalf("mapModelCancelError() = %v, want provider error", err)
	}
}
