package initiative

import (
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
