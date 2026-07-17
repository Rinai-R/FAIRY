package companion

import (
	"encoding/json"
	"testing"
)

func TestTurnLifecycleHappyPathJSONShape(t *testing.T) {
	life := NewTurnLifecycle("6a129284-6358-47b0-ad64-2a5907d36c91", "6a129284-6358-47b0-ad64-2a5907d36c92")
	for _, state := range []TurnState{TurnStateInterpreting, TurnStateGathering, TurnStatePlanning, TurnStateResponding} {
		event, err := life.Transition(state)
		if err != nil {
			t.Fatalf("Transition(%s) error = %v", state, err)
		}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		payload := decoded["payload"].(map[string]any)
		if len(payload) != 1 || payload["type"] != "state_changed" {
			t.Fatalf("state_changed payload = %#v", payload)
		}
	}
	chain := ReplyChain{Text: "我在。", SpeechText: "我在。", VisualState: "idle"}
	reply, err := life.ReplyChain(0, "我在。", chain)
	if err != nil {
		t.Fatalf("ReplyChain() error = %v", err)
	}
	if reply.Sequence != 5 {
		t.Fatalf("reply sequence = %d", reply.Sequence)
	}
	completed, err := life.Complete(TurnCompletion{
		Text:              "我在。",
		SpeechText:        "我在。",
		CharacterRevision: 2,
		VisualState:       "idle",
		Chains:            []ReplyChain{chain},
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	raw, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	payload := decoded["payload"].(map[string]any)
	if payload["type"] != "completed" || payload["userProfileRevision"] != nil {
		t.Fatalf("completed payload = %#v", payload)
	}
	if _, ok := payload["sources"]; !ok {
		t.Fatal("completed payload missing sources")
	}
}

func TestTurnLifecycleRejectsInvalidTransition(t *testing.T) {
	life := NewTurnLifecycle("c", "t")
	if _, err := life.Transition(TurnStateResponding); err == nil {
		t.Fatal("idle -> responding must fail")
	}
}

func TestCompletedUsageWireShapeMatchesFrontendContract(t *testing.T) {
	life := NewTurnLifecycle("6a129284-6358-47b0-ad64-2a5907d36c91", "6a129284-6358-47b0-ad64-2a5907d36c92")
	for _, state := range []TurnState{TurnStateInterpreting, TurnStateGathering, TurnStatePlanning, TurnStateResponding} {
		if _, err := life.Transition(state); err != nil {
			t.Fatalf("Transition(%s) error = %v", state, err)
		}
	}
	chain := ReplyChain{Text: "我在。", SpeechText: "我在。", VisualState: "idle"}
	if _, err := life.ReplyChain(0, "我在。", chain); err != nil {
		t.Fatalf("ReplyChain() error = %v", err)
	}
	input := uint64(12)
	output := uint64(4)
	completed, err := life.Complete(TurnCompletion{
		Text:              "我在。",
		SpeechText:        "我在。",
		CharacterRevision: 2,
		VisualState:       "idle",
		Chains:            []ReplyChain{chain},
		Usage: []LaneModelUsage{{
			Lane:          "respond",
			HistoryWindow: 1,
			Usage: LaneUsage{
				InputTokens:       &input,
				OutputTokens:      &output,
				CachedInputTokens: CacheMissing(),
				CacheWriteTokens:  CacheMissing(),
			},
		}},
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	raw, err := json.Marshal(completed)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	payload := decoded["payload"].(map[string]any)
	usageList, ok := payload["usage"].([]any)
	if !ok || len(usageList) != 1 {
		t.Fatalf("usage = %#v", payload["usage"])
	}
	entry := usageList[0].(map[string]any)
	usage := entry["usage"].(map[string]any)
	cached := usage["cachedInputTokens"].(map[string]any)
	if cached["status"] != "missing" {
		t.Fatalf("cachedInputTokens = %#v, want status missing", cached)
	}
	if _, hasTokens := cached["tokens"]; hasTokens {
		t.Fatalf("missing observation must omit tokens: %#v", cached)
	}
	write := usage["cacheWriteTokens"].(map[string]any)
	if write["status"] != "missing" {
		t.Fatalf("cacheWriteTokens = %#v, want status missing", write)
	}
	if usage["inputTokens"] != float64(12) || usage["outputTokens"] != float64(4) {
		t.Fatalf("token counts = %#v", usage)
	}
}
