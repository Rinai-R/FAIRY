//go:build sqlite_legacy

package memory

import (
	"fmt"
	"path/filepath"
	"testing"
)

func appendModelUsageEvent(t *testing.T, store *Store, conversationID string, turnID string, lane string, input uint64, output uint64, cached *uint64) {
	t.Helper()
	cachedJSON := `{"status":"missing","tokens":null}`
	if cached != nil {
		cachedJSON = fmt.Sprintf(`{"status":"observed","tokens":%d}`, *cached)
	}
	metadata := fmt.Sprintf(
		`{"streamEventCount":1,"usage":[{"lane":%q,"historyWindow":1,"usage":{"inputTokens":%d,"outputTokens":%d,"cachedInputTokens":%s,"cacheWriteTokens":{"status":"missing","tokens":null}}}]}`,
		lane, input, output, cachedJSON,
	)
	if _, err := store.AppendTurnRuntimeEvent(TurnRuntimeEventInput{
		ConversationID: conversationID,
		TurnID:         turnID,
		EventType:      "model",
		MetadataJSON:   metadata,
	}); err != nil {
		t.Fatalf("AppendTurnRuntimeEvent(model) error = %v", err)
	}
}

func appendTerminalEvent(t *testing.T, store *Store, conversationID string, turnID string, status string) {
	t.Helper()
	if _, err := store.AppendTurnRuntimeEvent(TurnRuntimeEventInput{
		ConversationID: conversationID,
		TurnID:         turnID,
		EventType:      "terminal",
		State:          &status,
		MetadataJSON:   fmt.Sprintf(`{"status":%q}`, status),
	}); err != nil {
		t.Fatalf("AppendTurnRuntimeEvent(terminal) error = %v", err)
	}
}

func findLane(t *testing.T, lanes []UsageLaneAggregate, lane string) UsageLaneAggregate {
	t.Helper()
	for _, aggregate := range lanes {
		if aggregate.Lane == lane {
			return aggregate
		}
	}
	t.Fatalf("lane %q not found in %#v", lane, lanes)
	return UsageLaneAggregate{}
}

func TestAggregateTokenUsageSumsRespondCallsPerTurn(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	turn, err := store.BeginTurn(bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	firstCache := uint64(400)
	secondCache := uint64(600)
	appendModelUsageEvent(t, store, bootstrap.Conversation.ID, turn.ID, "respond", 1000, 120, &firstCache)
	appendModelUsageEvent(t, store, bootstrap.Conversation.ID, turn.ID, "respond", 1500, 80, &secondCache)
	appendTerminalEvent(t, store, bootstrap.Conversation.ID, turn.ID, "completed")

	report, err := store.AggregateTokenUsage(0)
	if err != nil {
		t.Fatalf("AggregateTokenUsage() error = %v", err)
	}
	if report.TurnCount != 1 || len(report.Turns) != 1 {
		t.Fatalf("report = %#v, want single turn", report)
	}
	turnUsage := report.Turns[0]
	if turnUsage.Status != "completed" || turnUsage.CharacterID != "character-1" {
		t.Fatalf("turn usage = %#v", turnUsage)
	}
	respond := findLane(t, turnUsage.Lanes, "respond")
	if respond.InputTokens != 2500 || respond.OutputTokens != 200 || respond.CachedInputTokens != 1000 {
		t.Fatalf("respond lane = %#v, want input 2500 output 200 cached 1000", respond)
	}
	if respond.CachedObservedInputTokens != 2500 || respond.CallCount != 2 {
		t.Fatalf("respond lane observed/call = %#v", respond)
	}
	overall := findLane(t, report.Overall, "respond")
	if overall.InputTokens != 2500 || overall.CachedInputTokens != 1000 {
		t.Fatalf("overall respond = %#v", overall)
	}
}

func TestAggregateTokenUsageSumsAcrossConversations(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	for index, characterID := range []string{"character-1", "character-2"} {
		bootstrap, err := store.OpenOrCreateCharacterConversation(characterID)
		if err != nil {
			t.Fatalf("OpenOrCreateCharacterConversation(%s) error = %v", characterID, err)
		}
		turn, err := store.BeginTurn(bootstrap.Conversation.ID, fmt.Sprintf("消息 %d", index))
		if err != nil {
			t.Fatalf("BeginTurn() error = %v", err)
		}
		cache := uint64(100 * (index + 1))
		appendModelUsageEvent(t, store, bootstrap.Conversation.ID, turn.ID, "respond", uint64(500*(index+1)), uint64(50), &cache)
		appendTerminalEvent(t, store, bootstrap.Conversation.ID, turn.ID, "completed")
	}
	report, err := store.AggregateTokenUsage(0)
	if err != nil {
		t.Fatalf("AggregateTokenUsage() error = %v", err)
	}
	if report.TurnCount != 2 {
		t.Fatalf("turn count = %d, want 2", report.TurnCount)
	}
	overall := findLane(t, report.Overall, "respond")
	if overall.InputTokens != 1500 || overall.CachedInputTokens != 300 {
		t.Fatalf("overall respond = %#v, want input 1500 cached 300", overall)
	}
}

func TestAggregateTokenUsageIgnoresUnobservedCacheForHitRate(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	turn, err := store.BeginTurn(bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	appendModelUsageEvent(t, store, bootstrap.Conversation.ID, turn.ID, "respond", 900, 60, nil)
	appendTerminalEvent(t, store, bootstrap.Conversation.ID, turn.ID, "completed")

	report, err := store.AggregateTokenUsage(0)
	if err != nil {
		t.Fatalf("AggregateTokenUsage() error = %v", err)
	}
	respond := findLane(t, report.Turns[0].Lanes, "respond")
	if respond.InputTokens != 900 {
		t.Fatalf("input tokens = %d, want 900", respond.InputTokens)
	}
	if respond.CachedInputTokens != 0 || respond.CachedObservedInputTokens != 0 {
		t.Fatalf("unobserved cache leaked into hit rate: %#v", respond)
	}
}

func TestAggregateTokenUsageKeepsFailedTurnWithoutUsage(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	turn, err := store.BeginTurn(bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurn() error = %v", err)
	}
	appendTerminalEvent(t, store, bootstrap.Conversation.ID, turn.ID, "failed")

	report, err := store.AggregateTokenUsage(0)
	if err != nil {
		t.Fatalf("AggregateTokenUsage() error = %v", err)
	}
	if report.TurnCount != 1 || len(report.Turns) != 1 {
		t.Fatalf("report = %#v, want one turn", report)
	}
	if report.Turns[0].Status != "failed" || len(report.Turns[0].Lanes) != 0 {
		t.Fatalf("failed turn = %#v", report.Turns[0])
	}
	if len(report.Overall) != 0 {
		t.Fatalf("overall = %#v, want empty", report.Overall)
	}
}

func TestAggregateTokenUsageTruncatesDetailButCountsAll(t *testing.T) {
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	for index := 0; index < 3; index++ {
		turn, err := store.BeginTurn(bootstrap.Conversation.ID, fmt.Sprintf("消息 %d", index))
		if err != nil {
			t.Fatalf("BeginTurn() error = %v", err)
		}
		cache := uint64(10)
		appendModelUsageEvent(t, store, bootstrap.Conversation.ID, turn.ID, "respond", 100, 10, &cache)
		appendTerminalEvent(t, store, bootstrap.Conversation.ID, turn.ID, "completed")
	}
	report, err := store.AggregateTokenUsage(2)
	if err != nil {
		t.Fatalf("AggregateTokenUsage() error = %v", err)
	}
	if report.TurnCount != 3 || len(report.Turns) != 2 || !report.Truncated {
		t.Fatalf("report = %#v, want 3 counted, 2 returned, truncated", report)
	}
	overall := findLane(t, report.Overall, "respond")
	if overall.InputTokens != 300 {
		t.Fatalf("overall input = %d, want 300 (all turns)", overall.InputTokens)
	}
}
