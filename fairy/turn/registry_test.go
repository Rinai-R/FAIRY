package turn

import (
	"context"
	"errors"
	"testing"
)

func TestRegistryAdmissionAndDeliveryBoundary(t *testing.T) {
	r := NewRegistry(nil)
	ctx, err := r.Reserve("conversation")
	if err != nil {
		t.Fatal(err)
	}
	if ctx == nil {
		t.Fatal("Reserve returned nil context")
	}
	r.Bind("conversation", "turn-1")
	if canceled, err := r.CancelBeforeDelivery("conversation"); err != nil || !canceled {
		t.Fatalf("CancelBeforeDelivery() = %v, %v", canceled, err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("reserved context was not canceled")
	}
	r.End("conversation", "turn-1")

	ctx, err = r.Reserve("conversation")
	if err != nil {
		t.Fatal(err)
	}
	r.Bind("conversation", "turn-2")
	r.MarkDelivering("conversation", "turn-2")
	if canceled, err := r.CancelBeforeDelivery("conversation"); err != nil || canceled {
		t.Fatalf("delivering turn cancellation = %v, %v", canceled, err)
	}
	r.End("conversation", "turn-2")
}

func TestRegistryRejectsTurnAndCompactionConflicts(t *testing.T) {
	r := NewRegistry(nil)
	if err := r.BeginCompaction("conversation"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reserve("conversation"); !errors.Is(err, ErrInProgress) {
		t.Fatalf("Reserve() error = %v", err)
	}
	r.EndCompaction("conversation")
	if _, err := r.Reserve("conversation"); err != nil {
		t.Fatal(err)
	}
	if err := r.BeginCompaction("conversation"); !errors.Is(err, ErrInProgress) {
		t.Fatalf("BeginCompaction() error = %v", err)
	}
	r.End("conversation", "")
}

func TestRegistryCancelHookUsesTurnIdentity(t *testing.T) {
	called := false
	r := NewRegistry(func(ctx context.Context, conversationID, turnID string) error {
		called = true
		if conversationID != "conversation" || turnID != "turn-1" || ctx == nil {
			t.Fatalf("unexpected cancel hook arguments: %q %q %v", conversationID, turnID, ctx)
		}
		return nil
	})
	if _, err := r.Reserve("conversation"); err != nil {
		t.Fatal(err)
	}
	r.Bind("conversation", "turn-1")
	if err := r.Cancel("conversation", "turn-1"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("cancel hook was not called")
	}
}

func TestRegistryCancelBeforeDeliveryStillCancelsWhenHookFails(t *testing.T) {
	r := NewRegistry(func(context.Context, string, string) error {
		return errors.New("external cancellation failed")
	})
	turnCtx, err := r.Reserve("conversation")
	if err != nil {
		t.Fatal(err)
	}
	r.Bind("conversation", "turn-1")
	if canceled, err := r.CancelBeforeDelivery("conversation"); !canceled || err == nil {
		t.Fatalf("CancelBeforeDelivery() = %v, %v", canceled, err)
	}
	select {
	case <-turnCtx.Done():
	default:
		t.Fatal("turn context was not canceled after hook failure")
	}
}
