package companion

import (
	"context"
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
