package companion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRegistryAdmissionAndDeliveryBoundary(t *testing.T) {
	r := newTurnRegistry(nil)
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
	r := newTurnRegistry(nil)
	if err := r.BeginCompaction("conversation"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reserve("conversation"); !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("Reserve() error = %v", err)
	}
	r.EndCompaction("conversation")
	if _, err := r.Reserve("conversation"); err != nil {
		t.Fatal(err)
	}
	if err := r.BeginCompaction("conversation"); !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("BeginCompaction() error = %v", err)
	}
	r.End("conversation", "")
}

func TestRegistryCancelHookUsesTurnIdentity(t *testing.T) {
	called := false
	r := newTurnRegistry(func(ctx context.Context, conversationID, turnID string) error {
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
	r := newTurnRegistry(func(context.Context, string, string) error {
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

func registryGateCount(registry *turnRegistry) int {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	return len(registry.gates)
}

func TestRegistryReclaimsCompletedTurnAndCompactionGates(t *testing.T) {
	registry := newTurnRegistry(nil)
	for index := 0; index < 1000; index++ {
		conversationID := fmt.Sprintf("turn-conversation-%d", index)
		if _, err := registry.Reserve(conversationID); err != nil {
			t.Fatal(err)
		}
		registry.Bind(conversationID, fmt.Sprintf("turn-%d", index))
		registry.End(conversationID, fmt.Sprintf("turn-%d", index))
	}
	if count := registryGateCount(registry); count != 0 {
		t.Fatalf("turn gates after completion = %d, want 0", count)
	}
	for index := 0; index < 1000; index++ {
		conversationID := fmt.Sprintf("compaction-conversation-%d", index)
		if err := registry.BeginCompaction(conversationID); err != nil {
			t.Fatal(err)
		}
		registry.EndCompaction(conversationID)
	}
	if count := registryGateCount(registry); count != 0 {
		t.Fatalf("compaction gates after completion = %d, want 0", count)
	}
}

func TestRegistryUnknownNonCreatingOperationsDoNotRetainGates(t *testing.T) {
	registry := newTurnRegistry(nil)
	registry.Bind("bind", "turn")
	registry.MarkDelivering("mark", "turn")
	registry.End("end", "turn")
	registry.EndCompaction("compaction")
	if err := registry.Cancel("cancel", "turn"); !errors.Is(err, ErrTurnNotActive) {
		t.Fatalf("Cancel unknown = %v", err)
	}
	if canceled, err := registry.CancelBeforeDelivery("cancel-before"); err != nil || canceled {
		t.Fatalf("CancelBeforeDelivery unknown = %v, %v", canceled, err)
	}
	if count := registryGateCount(registry); count != 0 {
		t.Fatalf("unknown operations retained %d gates", count)
	}
}

func TestRegistryCancelAllReclaimsCanceledAndIdleGates(t *testing.T) {
	registry := newTurnRegistry(nil)
	contexts := make([]context.Context, 0, 64)
	for index := 0; index < 64; index++ {
		conversationID := fmt.Sprintf("conversation-%d", index)
		ctx, err := registry.Reserve(conversationID)
		if err != nil {
			t.Fatal(err)
		}
		contexts = append(contexts, ctx)
	}
	registry.CancelAll()
	for index, ctx := range contexts {
		select {
		case <-ctx.Done():
		default:
			t.Fatalf("context %d was not canceled", index)
		}
	}
	if count := registryGateCount(registry); count != 0 {
		t.Fatalf("gates after CancelAll = %d, want 0", count)
	}
}

func TestRegistryConcurrentEndAndReserveNeverSplitAdmission(t *testing.T) {
	for round := 0; round < 100; round++ {
		registry := newTurnRegistry(nil)
		if _, err := registry.Reserve("conversation"); err != nil {
			t.Fatal(err)
		}
		registry.Bind("conversation", "initial")
		start := make(chan struct{})
		var admitted atomic.Int64
		var concurrent sync.WaitGroup
		concurrent.Add(33)
		go func() {
			defer concurrent.Done()
			<-start
			registry.End("conversation", "initial")
		}()
		for index := 0; index < 32; index++ {
			go func() {
				defer concurrent.Done()
				<-start
				if _, err := registry.Reserve("conversation"); err == nil {
					admitted.Add(1)
				} else if !errors.Is(err, ErrTurnInProgress) {
					t.Errorf("Reserve error = %v", err)
				}
			}()
		}
		close(start)
		concurrent.Wait()
		if got := admitted.Load(); got > 1 {
			t.Fatalf("round %d admitted %d concurrent turns", round, got)
		}
		registry.End("conversation", "")
		if admitted.Load() == 0 {
			if _, err := registry.Reserve("conversation"); err != nil {
				t.Fatalf("round %d post-race Reserve = %v", round, err)
			}
			registry.End("conversation", "")
		}
		if count := registryGateCount(registry); count != 0 {
			t.Fatalf("round %d retained %d gates", round, count)
		}
	}
}
