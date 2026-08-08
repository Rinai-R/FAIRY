package conversation

import (
	"fairy/agent/conversation/delivery"
	"fairy/agent/conversation/lifecycle"
	"fairy/agent/tool"
	"testing"
)

func TestToolUtteranceReason(t *testing.T) {
	if delivery.ToolUtteranceReason(tool.MemorySearch) != "searching_memory" {
		t.Fatal("memory tool reason")
	}
	if delivery.ToolUtteranceReason(tool.WebSearch) != "searching_web" {
		t.Fatal("web tool reason")
	}
	if delivery.ToolUtteranceReason("other") != "thinking" {
		t.Fatal("default reason")
	}
}

func TestLifecycleUtteranceInPlanning(t *testing.T) {
	life := lifecycle.New("c1", "t1")
	if _, err := life.Transition(lifecycle.StateInterpreting); err != nil {
		t.Fatal(err)
	}
	if _, err := life.Transition(lifecycle.StateGathering); err != nil {
		t.Fatal(err)
	}
	if _, err := life.Transition(lifecycle.StatePlanning); err != nil {
		t.Fatal(err)
	}
	event, err := life.Utterance(0, "角色自己的等待句。", "idle", "thinking")
	if err != nil {
		t.Fatalf("Utterance() error = %v", err)
	}
	payload := decodeEventPayload[lifecycle.UtterancePayload](t, event.Payload)
	if payload.Type != "utterance" || payload.Reason != "thinking" {
		t.Fatalf("payload = %#v", event.Payload)
	}
}

func TestCompileReplyKeepsOnlyFirstChain(t *testing.T) {
	t.Skip("multi-chain replies are restored; truncation-to-first is no longer desired")
}
