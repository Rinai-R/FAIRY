package companion

import (
	"path/filepath"
	"strings"
	"testing"

	"fairy/character"
	"fairy/memory"
	"fairy/model"
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
	items, err := BuildCompactInput(
		character.Record{CharacterID: "character-1", Revision: 2, Name: "亚托莉", Description: "认真听用户说话。"},
		nil,
		memory.PromptWindowRecord{Revision: 2, Summary: &summary, CutoffMessageSequence: 2},
		[]memory.MessageRecord{
			{Role: "user", Content: "旧", Sequence: 1},
			{Role: "assistant", Content: "旧答", Sequence: 2},
			{Role: "user", Content: "新对话", Sequence: 3},
		},
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
	if strings.Contains(joined, "旧答") {
		t.Fatalf("cutoff dialogue leaked: %s", joined)
	}
	if items[0].Type != model.PromptItemContextData {
		t.Fatalf("first item = %#v", items[0])
	}
}
