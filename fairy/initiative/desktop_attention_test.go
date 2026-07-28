package initiative

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDesktopAttentionBudgetAndSpacing(t *testing.T) {
	e := NewAttentionEvaluator()
	now := time.UnixMilli(100000)
	rulebook := DesktopRulebook{AttentionBudget: 1, MinSpacing: time.Minute}
	if action, err := e.Evaluate("c1", DesktopActionInitiate, rulebook, now); err != nil || action != DesktopActionInitiate {
		t.Fatalf("first = %v, %v", action, err)
	}
	if action, err := e.Evaluate("c1", DesktopActionInitiate, rulebook, now.Add(time.Second)); err != nil || action != DesktopActionSilent {
		t.Fatalf("second = %v, %v", action, err)
	}
}

func TestDesktopAttentionDoesNotRetainSilentOrReactState(t *testing.T) {
	e := newAttentionEvaluator(2)
	now := time.UnixMilli(100000)
	rulebook := DesktopRulebook{AttentionBudget: 1, MinSpacing: time.Minute}
	for index := 0; index < 10; index++ {
		for _, action := range []DesktopObservationAction{DesktopActionSilent, DesktopActionReact} {
			got, err := e.Evaluate(fmt.Sprintf("conversation-%d-%s", index, action), action, rulebook, now)
			if err != nil || got != action {
				t.Fatalf("Evaluate(%s) = %s, %v", action, got, err)
			}
		}
	}
	if got := attentionStateCount(e); got != 0 {
		t.Fatalf("attention states = %d, want 0", got)
	}
}

func TestDesktopAttentionSaturatesToSilentWithoutEvictingEffectiveState(t *testing.T) {
	e := newAttentionEvaluator(2)
	now := time.UnixMilli(100000)
	rulebook := DesktopRulebook{AttentionBudget: 1, MinSpacing: time.Minute}
	for _, conversationID := range []string{"conversation-1", "conversation-2"} {
		action, err := e.Evaluate(conversationID, DesktopActionInitiate, rulebook, now)
		if err != nil || action != DesktopActionInitiate {
			t.Fatalf("Evaluate(%q) = %s, %v", conversationID, action, err)
		}
	}
	action, err := e.Evaluate("conversation-3", DesktopActionInitiate, rulebook, now)
	if err != nil || action != DesktopActionSilent {
		t.Fatalf("saturated action = %s, %v", action, err)
	}
	e.mu.Lock()
	_, firstFound := e.states["conversation-1"]
	_, secondFound := e.states["conversation-2"]
	_, thirdFound := e.states["conversation-3"]
	stateCount := len(e.states)
	e.mu.Unlock()
	if !firstFound || !secondFound || thirdFound || stateCount != 2 {
		t.Fatalf("states after saturation: first=%t second=%t third=%t count=%d", firstFound, secondFound, thirdFound, stateCount)
	}
	action, err = e.Evaluate("conversation-1", DesktopActionInitiate, rulebook, now.Add(time.Second))
	if err != nil || action != DesktopActionSilent {
		t.Fatalf("existing budget after saturation = %s, %v", action, err)
	}
}

func TestDesktopAttentionReclaimsFullyExpiredState(t *testing.T) {
	e := newAttentionEvaluator(1)
	now := time.UnixMilli(100000)
	rulebook := DesktopRulebook{AttentionBudget: 1, MinSpacing: time.Minute}
	if action, err := e.Evaluate("conversation-old", DesktopActionInitiate, rulebook, now); err != nil || action != DesktopActionInitiate {
		t.Fatalf("old initiate = %s, %v", action, err)
	}
	if action, err := e.Evaluate("conversation-new", DesktopActionInitiate, rulebook, now.Add(59*time.Minute)); err != nil || action != DesktopActionSilent {
		t.Fatalf("pre-expiry admission = %s, %v", action, err)
	}
	if action, err := e.Evaluate("conversation-new", DesktopActionInitiate, rulebook, now.Add(time.Hour)); err != nil || action != DesktopActionInitiate {
		t.Fatalf("post-expiry admission = %s, %v", action, err)
	}
	e.mu.Lock()
	_, oldFound := e.states["conversation-old"]
	_, newFound := e.states["conversation-new"]
	stateCount := len(e.states)
	e.mu.Unlock()
	if oldFound || !newFound || stateCount != 1 {
		t.Fatalf("states after expiry: old=%t new=%t count=%d", oldFound, newFound, stateCount)
	}
}

func TestDesktopAttentionKeepsStateUntilLongSpacingExpires(t *testing.T) {
	e := newAttentionEvaluator(1)
	now := time.UnixMilli(100000)
	rulebook := DesktopRulebook{AttentionBudget: 1, MinSpacing: 2 * time.Hour}
	if action, err := e.Evaluate("conversation-old", DesktopActionInitiate, rulebook, now); err != nil || action != DesktopActionInitiate {
		t.Fatalf("old initiate = %s, %v", action, err)
	}
	midpoint := now.Add(time.Hour + time.Minute)
	if action, err := e.Evaluate("conversation-old", DesktopActionInitiate, rulebook, midpoint); err != nil || action != DesktopActionSilent {
		t.Fatalf("long-spacing existing action = %s, %v", action, err)
	}
	if action, err := e.Evaluate("conversation-new", DesktopActionInitiate, rulebook, midpoint); err != nil || action != DesktopActionSilent {
		t.Fatalf("long-spacing capacity action = %s, %v", action, err)
	}
	if action, err := e.Evaluate("conversation-new", DesktopActionInitiate, rulebook, now.Add(2*time.Hour)); err != nil || action != DesktopActionInitiate {
		t.Fatalf("post-spacing action = %s, %v", action, err)
	}
}

func TestDesktopAttentionConcurrentAdmissionRemainsBounded(t *testing.T) {
	const capacity = 8
	e := newAttentionEvaluator(capacity)
	now := time.UnixMilli(100000)
	rulebook := DesktopRulebook{AttentionBudget: 1, MinSpacing: time.Minute}
	var initiated atomic.Int32
	var workers sync.WaitGroup
	for index := 0; index < 64; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			action, err := e.Evaluate(fmt.Sprintf("conversation-%d", index), DesktopActionInitiate, rulebook, now)
			if err != nil {
				t.Errorf("Evaluate(%d): %v", index, err)
				return
			}
			if action == DesktopActionInitiate {
				initiated.Add(1)
			}
		}(index)
	}
	workers.Wait()
	if got := initiated.Load(); got != capacity {
		t.Fatalf("initiated = %d, want %d", got, capacity)
	}
	if got := attentionStateCount(e); got != capacity {
		t.Fatalf("attention states = %d, want %d", got, capacity)
	}
}

func attentionStateCount(e *AttentionEvaluator) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.states)
}
