package conversation

import (
	"bytes"
	"encoding/json"
	"testing"

	"fairy/agent/conversation/lifecycle"
	"fairy/agent/reply"
	"fairy/runtime/model"
)

func TestTurnLifecycleHappyPathJSONShape(t *testing.T) {
	life := lifecycle.New("6a129284-6358-47b0-ad64-2a5907d36c91", "6a129284-6358-47b0-ad64-2a5907d36c92")
	for _, state := range []lifecycle.State{lifecycle.StateInterpreting, lifecycle.StateGathering, lifecycle.StatePlanning, lifecycle.StateResponding} {
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
	chain := reply.ReplyChain{Text: "我在。", VisualState: "idle"}
	replyEvent, err := life.ReplyChain(0, "我在。", chain)
	if err != nil {
		t.Fatalf("ReplyChain() error = %v", err)
	}
	if replyEvent.Sequence != 5 {
		t.Fatalf("reply sequence = %d", replyEvent.Sequence)
	}
	completed, err := life.Complete(lifecycle.Completion{
		Text:              "我在。",
		CharacterRevision: 2,
		VisualState:       "idle",
		Chains:            []reply.ReplyChain{chain},
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
	life := lifecycle.New("c", "t")
	if _, err := life.Transition(lifecycle.StateResponding); err == nil {
		t.Fatal("idle -> responding must fail")
	}
}

func TestTurnLifecycleTemporaryStreamEventsAreOrderedAndMarked(t *testing.T) {
	life := lifecycle.New("conversation", "turn")
	for _, state := range []lifecycle.State{lifecycle.StateInterpreting, lifecycle.StateGathering, lifecycle.StatePlanning} {
		if _, err := life.Transition(state); err != nil {
			t.Fatalf("Transition(%s) error = %v", state, err)
		}
	}
	presence, err := life.Presence("model_stream")
	if err != nil {
		t.Fatalf("Presence() error = %v", err)
	}
	preview, err := life.ReplyPreview([]reply.ReplyChain{{Text: "完整文本", VisualState: "idle"}})
	if err != nil {
		t.Fatalf("ReplyPreview() error = %v", err)
	}
	if presence.Sequence >= preview.Sequence {
		t.Fatalf("temporary event order = presence %d, preview %d", presence.Sequence, preview.Sequence)
	}
	if payload := decodeEventPayload[lifecycle.PresencePayload](t, presence.Payload); payload.Type != "presence" || payload.Phase != "model_stream" {
		t.Fatalf("presence payload = %#v", presence.Payload)
	}
	if payload := decodeEventPayload[lifecycle.ReplyPreviewPayload](t, preview.Payload); payload.Type != "reply.preview" || len(payload.Chains) != 1 {
		t.Fatalf("preview payload = %#v", preview.Payload)
	}
}

func TestTurnLifecycleRejectsUnsafeTemporaryEvents(t *testing.T) {
	life := lifecycle.New("conversation", "turn")
	if _, err := life.Presence("model_stream"); err == nil {
		t.Fatal("Presence() in idle error = nil")
	}
	for _, state := range []lifecycle.State{lifecycle.StateInterpreting, lifecycle.StateGathering, lifecycle.StatePlanning} {
		if _, err := life.Transition(state); err != nil {
			t.Fatalf("Transition(%s) error = %v", state, err)
		}
	}
	if _, err := life.ReplyPreview(nil); err == nil {
		t.Fatal("ReplyPreview(nil) error = nil")
	}
}

func TestBeatReadyIncludesPacingFields(t *testing.T) {
	life := lifecycle.New("conversation", "turn")
	for _, state := range []lifecycle.State{lifecycle.StateInterpreting, lifecycle.StateGathering, lifecycle.StatePlanning, lifecycle.StateResponding} {
		if _, err := life.Transition(state); err != nil {
			t.Fatalf("Transition(%s) error = %v", state, err)
		}
	}
	event, err := life.BeatReady(reply.BeatReadyCompletion{
		BeatID:               "final-1",
		Kind:                 reply.BeatKindFinal,
		Index:                1,
		ChainIndex:           1,
		DisplayText:          "第二拍",
		VisualState:          "idle",
		TargetIntervalMS:     920,
		PaceWaitMS:           370,
		PublishedPrefixCount: 2,
		ReplyTargetMessageID: "qq-message-42",
	})
	if err != nil {
		t.Fatalf("BeatReady() error = %v", err)
	}
	payload := decodeEventPayload[lifecycle.BeatReadyPayload](t, event.Payload)
	if payload.TargetIntervalMS != 920 || payload.PaceWaitMS != 370 || payload.PublishedPrefixCount != 2 || payload.ReplyTargetMessageID != "qq-message-42" {
		t.Fatalf("pacing payload = %#v", payload)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, field := range []string{`"targetIntervalMs":920`, `"paceWaitMs":370`, `"publishedPrefixCount":2`, `"replyTargetMessageId":"qq-message-42"`} {
		if !bytes.Contains(raw, []byte(field)) {
			t.Fatalf("wire event missing %s: %s", field, raw)
		}
	}
}

func TestStickerExpressionEventsAllowEmptyTextAndCarrySnapshot(t *testing.T) {
	life := lifecycle.New("conversation", "turn")
	for _, state := range []lifecycle.State{lifecycle.StateInterpreting, lifecycle.StateGathering, lifecycle.StatePlanning, lifecycle.StateResponding} {
		if _, err := life.Transition(state); err != nil {
			t.Fatal(err)
		}
	}
	chain := reply.ReplyChain{
		Kind: reply.ChainSticker, VisualState: "surprised",
		Sticker: &reply.StickerReference{ID: "sticker-1", Description: "震惊", MIMEType: "image/png"},
	}
	chainEvent, err := life.ReplyChain(0, "", chain)
	if err != nil {
		t.Fatal(err)
	}
	chainPayload := decodeEventPayload[lifecycle.ReplyChainPayload](t, chainEvent.Payload)
	if chainPayload.Delta != "" || chainPayload.Part.Kind != "sticker" || chainPayload.Part.Sticker.ID != "sticker-1" {
		t.Fatalf("reply chain payload = %#v", chainPayload)
	}
	beat, err := life.BeatReady(reply.BeatReadyCompletion{
		BeatID: "final-0", Kind: reply.BeatKindFinal, ChainIndex: 0, VisualState: "surprised", Chain: &chain,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeEventPayload[lifecycle.BeatReadyPayload](t, beat.Payload)
	if payload.DisplayText != "" || payload.Part == nil || payload.Part.Kind != "sticker" ||
		payload.Part.Sticker == nil || payload.Part.Sticker.Description != "震惊" {
		t.Fatalf("sticker beat payload = %#v", payload)
	}
}

func TestCompletedUsageWireShapeMatchesFrontendContract(t *testing.T) {
	life := lifecycle.New("6a129284-6358-47b0-ad64-2a5907d36c91", "6a129284-6358-47b0-ad64-2a5907d36c92")
	for _, state := range []lifecycle.State{lifecycle.StateInterpreting, lifecycle.StateGathering, lifecycle.StatePlanning, lifecycle.StateResponding} {
		if _, err := life.Transition(state); err != nil {
			t.Fatalf("Transition(%s) error = %v", state, err)
		}
	}
	chain := reply.ReplyChain{Text: "我在。", VisualState: "idle"}
	if _, err := life.ReplyChain(0, "我在。", chain); err != nil {
		t.Fatalf("ReplyChain() error = %v", err)
	}
	input := uint64(12)
	output := uint64(4)
	completed, err := life.Complete(lifecycle.Completion{
		Text:              "我在。",
		CharacterRevision: 2,
		VisualState:       "idle",
		Chains:            []reply.ReplyChain{chain},
		Usage: []LaneModelUsage{{
			Lane:          "respond",
			HistoryWindow: 1,
			Usage: LaneUsage{
				InputTokens:       &input,
				OutputTokens:      &output,
				CachedInputTokens: model.CacheMissing(),
				CacheWriteTokens:  model.CacheMissing(),
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
