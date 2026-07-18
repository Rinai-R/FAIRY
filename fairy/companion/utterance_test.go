package companion

import "testing"

func TestToolUtteranceReason(t *testing.T) {
	if toolUtteranceReason(toolMemorySearch) != "searching_memory" {
		t.Fatal("memory tool reason")
	}
	if toolUtteranceReason(toolWebSearch) != "searching_web" {
		t.Fatal("web tool reason")
	}
	if toolUtteranceReason("other") != "thinking" {
		t.Fatal("default reason")
	}
}

func TestLifecycleUtteranceInPlanning(t *testing.T) {
	life := NewTurnLifecycle("c1", "t1")
	if _, err := life.Transition(TurnStateInterpreting); err != nil {
		t.Fatal(err)
	}
	if _, err := life.Transition(TurnStateGathering); err != nil {
		t.Fatal(err)
	}
	if _, err := life.Transition(TurnStatePlanning); err != nil {
		t.Fatal(err)
	}
	event, err := life.Utterance(0, "角色自己的等待句。", "idle", "thinking")
	if err != nil {
		t.Fatalf("Utterance() error = %v", err)
	}
	payload, ok := event.Payload.(utterancePayload)
	if !ok || payload.Type != "utterance" || payload.Reason != "thinking" {
		t.Fatalf("payload = %#v", event.Payload)
	}
}

func TestCompileReplyKeepsOnlyFirstChain(t *testing.T) {
	t.Skip("multi-chain replies are restored; truncation-to-first is no longer desired")
}
