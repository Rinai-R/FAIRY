package conversation

import (
	"context"
	"errors"
	"testing"
)

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
