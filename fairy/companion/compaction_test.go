package companion

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"fairy/character"
	"fairy/memory"
	"fairy/model"
	"fairy/profile"
)

func TestCompactionPolicyFromContextWindowUsesBasisPoints(t *testing.T) {
	policy := CompactionPolicyFromContextWindow(1_048_576)
	if policy.AutoInputTokenThreshold == nil {
		t.Fatal("threshold is nil")
	}
	want := uint64(1_048_576*8_000/10_000 - 640)
	if *policy.AutoInputTokenThreshold != want {
		t.Fatalf("threshold = %d, want %d", *policy.AutoInputTokenThreshold, want)
	}
	if policy.ShouldCompact(CompactionTriggerAfterCompletedTurn, want-1, true) {
		t.Fatal("below threshold should not compact")
	}
	if !policy.ShouldCompact(CompactionTriggerAfterCompletedTurn, want, true) {
		t.Fatal("at threshold should compact")
	}
	if policy.ShouldCompact(CompactionTriggerAfterCompletedTurn, want, false) {
		t.Fatal("unknown usage must never guess compaction")
	}
	if !policy.ShouldCompact(CompactionTriggerPreTurnPredictive, want, false) {
		t.Fatal("predictive trigger should use explicit local estimate")
	}
	if !policy.ShouldCompact(CompactionTriggerManual, 0, false) {
		t.Fatal("manual trigger should always compact")
	}
}

func TestCompactionPolicyUsesContextWindowFailureBreaker(t *testing.T) {
	threshold := uint64(10)
	policy := CompactionPolicy{AutoInputTokenThreshold: &threshold}
	belowBreaker := &memory.ContextWindowRecord{FailureCount: compactionFailureBreakerThreshold - 1}
	if !policy.ShouldCompactWindow(CompactionTriggerAfterCompletedTurn, threshold, true, belowBreaker) {
		t.Fatal("below breaker threshold should allow automatic compaction")
	}
	openBreaker := &memory.ContextWindowRecord{FailureCount: compactionFailureBreakerThreshold}
	if policy.ShouldCompactWindow(CompactionTriggerAfterCompletedTurn, threshold, true, openBreaker) {
		t.Fatal("open breaker should stop automatic compaction")
	}
	if !policy.ShouldCompactWindow(CompactionTriggerManual, 0, false, openBreaker) {
		t.Fatal("manual compaction must bypass automatic breaker")
	}
}

func TestEstimatePromptPrefillTokensIsExplicitAndNonZero(t *testing.T) {
	estimated := estimatePromptPrefillTokens("system", []model.PromptItem{
		{Type: model.PromptItemUserMessage, Content: "你好"},
		{Type: model.PromptItemAssistantMessage, Content: "我在。"},
	})
	if estimated == 0 {
		t.Fatal("estimated prompt tokens = 0, want explicit positive estimate")
	}
}

func TestRecordContextWindowFailureIncrementsBreakerCount(t *testing.T) {
	store, err := memory.OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	observed := uint64(20)
	if _, err := store.SaveContextWindow(memory.ContextWindowRecord{
		ConversationID:        bootstrap.Conversation.ID,
		Lane:                  string(model.PromptLaneRespond),
		WindowNumber:          1,
		FirstWindowID:         "window-1",
		WindowID:              "window-1",
		ObservedPrefillTokens: &observed,
		LastTrigger:           contextWindowTriggerCompletedUsage,
		PromptWindowRevision:  bootstrap.PromptWindow.Revision,
	}); err != nil {
		t.Fatalf("SaveContextWindow() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(t.TempDir(), store, nil)
	if err := service.recordContextWindowFailure(bootstrap.Conversation.ID); err != nil {
		t.Fatalf("recordContextWindowFailure() error = %v", err)
	}
	loaded, ok, err := store.LoadContextWindow(bootstrap.Conversation.ID, string(model.PromptLaneRespond))
	if err != nil {
		t.Fatalf("LoadContextWindow() error = %v", err)
	}
	if !ok || loaded.FailureCount != 1 || loaded.LastTrigger != contextWindowTriggerCompactionFailed {
		t.Fatalf("context window after failure = %#v ok=%v", loaded, ok)
	}
}

func TestNormalizeCompactionSummary(t *testing.T) {
	value, err := normalizeCompactionSummary("  用户正在讨论自己的近况。  ")
	if err != nil || value != "用户正在讨论自己的近况。" {
		t.Fatalf("normalize = %q, %v", value, err)
	}
	if _, err := normalizeCompactionSummary("   "); err == nil {
		t.Fatal("empty summary must fail")
	}
}

func TestBuildCompactInputIncludesCharacterProfileAndWindow(t *testing.T) {
	summary := "此前摘要"
	states := []VisualState{{ID: "idle", Description: "待机"}}
	items, err := BuildCompactInput(
		character.Record{CharacterID: "character-1", Revision: 2, Name: "亚托莉", Description: "认真听用户说话。"},
		nil,
		memory.PromptWindowRecord{Revision: 2, Summary: &summary, CutoffMessageSequence: 2},
		[]memory.MessageRecord{
			{Role: "user", Content: "旧", Sequence: 1},
			{Role: "assistant", Content: "旧答", Sequence: 2},
			{Role: "user", Content: "新对话", Sequence: 3},
		},
		states,
	)
	if err != nil {
		t.Fatalf("BuildCompactInput() error = %v", err)
	}
	joined := ""
	for _, item := range items {
		joined += item.Content + "\n"
	}
	if !strings.Contains(joined, "亚托莉") || !strings.Contains(joined, "compaction_summary") || !strings.Contains(joined, "新对话") {
		t.Fatalf("compact input incomplete: %s", joined)
	}
	if !strings.Contains(joined, "available_visual_states") || !strings.Contains(joined, "idle") {
		t.Fatalf("compact input missing shared visual prefix: %s", joined)
	}
	if strings.Contains(joined, "旧答") {
		t.Fatalf("cutoff dialogue leaked: %s", joined)
	}
	if items[0].Type != model.PromptItemContextData {
		t.Fatalf("first item = %#v", items[0])
	}
	last := items[len(items)-1]
	if last.Type != model.PromptItemUserMessage || last.Content != CompactInstructions {
		t.Fatalf("trailing compaction directive = %#v", last)
	}
}

func TestBuildCompactInputSharesRespondStablePrefixBytes(t *testing.T) {
	record := character.Record{CharacterID: "character-1", Revision: 2, Name: "亚托莉", Description: "认真听用户说话。"}
	profileSnapshot := &profile.Snapshot{Revision: 1, PreferredName: strPtr("Rinai")}
	states := []VisualState{{ID: "idle", Description: "待机"}, {ID: "talk", Description: "说话"}}
	summary := "此前摘要"

	respondSlots, err := BuildRespondContextSlots(
		record,
		profileSnapshot,
		memory.PromptWindowRecord{Revision: 2, Summary: &summary, CutoffMessageSequence: 1},
		[]memory.MessageRecord{{Role: "user", Content: "你好", Sequence: 2}},
		states,
		memory.RetrievalContext{PersonalMemories: []memory.RetrievedPersonalMemory{{
			ID:                    "memory-1",
			Kind:                  "preference",
			Scope:                 memory.MemoryScope{Type: "global"},
			Content:               "不该进稳定前缀",
			ConfidenceBasisPoints: 9000,
		}}},
	)
	if err != nil {
		t.Fatalf("BuildRespondContextSlots() error = %v", err)
	}
	compactItems, err := BuildCompactInput(
		record,
		profileSnapshot,
		memory.PromptWindowRecord{Revision: 2, Summary: &summary, CutoffMessageSequence: 1},
		[]memory.MessageRecord{{Role: "user", Content: "你好", Sequence: 2}},
		states,
	)
	if err != nil {
		t.Fatalf("BuildCompactInput() error = %v", err)
	}
	if len(compactItems) < 3 {
		t.Fatalf("compact items = %#v", compactItems)
	}
	for i, slotID := range []string{"character", "profile", "available_visual_states"} {
		if !respondSlots[i].Present || len(respondSlots[i].Items) != 1 {
			t.Fatalf("respond slot %s = %#v", slotID, respondSlots[i])
		}
		if compactItems[i] != respondSlots[i].Items[0] {
			t.Fatalf("stable prefix mismatch at %s\ncompact=%#v\nrespond=%#v", slotID, compactItems[i], respondSlots[i].Items[0])
		}
	}
	if respondSlots[5].ID != "retrieved_context" || respondSlots[5].CachePolicy != "tail" {
		t.Fatalf("retrieval must stay in tail: %#v", respondSlots[5])
	}
}

func TestBuildStablePrefixItemsIsDeterministic(t *testing.T) {
	record := character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "认真听用户说话。"}
	states := []VisualState{{ID: "idle", Description: "待机"}}
	first, err := BuildStablePrefixItems(record, nil, states)
	if err != nil {
		t.Fatalf("first BuildStablePrefixItems() error = %v", err)
	}
	second, err := BuildStablePrefixItems(record, nil, states)
	if err != nil {
		t.Fatalf("second BuildStablePrefixItems() error = %v", err)
	}
	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("prefix lens = %d,%d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("prefix item %d unstable: %#v vs %#v", i, first[i], second[i])
		}
	}
}

func strPtr(value string) *string { return &value }

func TestCompactConversationUsesRespondPromptCacheKey(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"用户问候，角色回应在场。\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_compact\",\"output\":[{\"type\":\"message\",\"content\":[{\"text\":\"用户问候，角色回应在场。\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":4}}}\n\n")
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeModelConnectionCapabilities(t, root, "responses", server.URL, false)
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	turn, err := memoryStore.BeginTurn(bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	if _, err := memoryStore.CompleteTurn(bootstrap.Conversation.ID, turn.ID, "我在。"); err != nil {
		t.Fatalf("CompleteTurn() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}))
	if _, err := service.CompactConversation(bootstrap.Conversation.ID); err != nil {
		t.Fatalf("CompactConversation() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("bodies = %d, want 1", len(bodies))
	}
	if bodies[0]["instructions"] != CompactInstructions {
		t.Fatalf("instructions = %#v", bodies[0]["instructions"])
	}
	wantKey := model.LaneCacheKey(bootstrap.Conversation.ID, model.PromptLaneRespond)
	if bodies[0]["prompt_cache_key"] != wantKey {
		t.Fatalf("prompt_cache_key = %#v, want %q", bodies[0]["prompt_cache_key"], wantKey)
	}
	if wantKey == model.LaneCacheKey(bootstrap.Conversation.ID, model.PromptLaneCompact) {
		t.Fatal("respond and compact lane keys must differ; test would be inconclusive")
	}
	input, ok := bodies[0]["input"].([]any)
	if !ok || len(input) < 4 {
		t.Fatalf("compact input = %#v", bodies[0]["input"])
	}
	last, _ := input[len(input)-1].(map[string]any)
	if last["role"] != "user" || last["content"] != CompactInstructions {
		t.Fatalf("trailing directive = %#v", last)
	}
}
