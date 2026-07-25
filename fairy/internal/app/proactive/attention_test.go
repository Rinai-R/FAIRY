package proactive

import (
	appobs "fairy/internal/app/observation"
	"testing"
	"time"
)

func TestDesktopAttentionBudgetAndSpacing(t *testing.T) {
	e := NewAttentionEvaluator()
	now := time.UnixMilli(100000)
	rulebook := appobs.DesktopRulebook{AttentionBudget: 1, MinSpacing: time.Minute}
	plan := appobs.DesktopGraphPlan{Action: appobs.DesktopActionInitiate}
	if action, err := e.Evaluate("c1", plan, rulebook, now); err != nil || action != appobs.DesktopActionInitiate {
		t.Fatalf("first = %v, %v", action, err)
	}
	if action, err := e.Evaluate("c1", plan, rulebook, now.Add(time.Second)); err != nil || action != appobs.DesktopActionSilent {
		t.Fatalf("second = %v, %v", action, err)
	}
}
