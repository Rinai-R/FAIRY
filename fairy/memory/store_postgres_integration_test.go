//go:build integration

package memory

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pgstore "fairy/postgres"
	"fairy/vectorindex"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresStoreSummaryUsesInjectedPool(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_, err := pool.Raw().Exec(ctx, `
INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
VALUES ('conversation-1', 'character-1', 1, 1);
INSERT INTO conversation_turns(id, conversation_id, sequence, status, extraction_state, created_at_ms, updated_at_ms)
VALUES ('turn-1', 'conversation-1', 1, 'completed', 'pending', 1, 1);
INSERT INTO personal_memories(id, kind, scope_kind, character_id, review_status, content, status, confidence_basis_points, source_conversation_id, source_turn_id, created_at_ms, updated_at_ms)
VALUES
  ('memory-global', 'fact', 'global', NULL, 'ready', '全局记忆', 'active', 9000, 'conversation-1', 'turn-1', 1, 1),
  ('memory-legacy', 'fact', 'unassigned_legacy', NULL, 'needs_review', '待归档记忆', 'active', 8000, 'conversation-1', 'turn-1', 1, 1);
INSERT INTO knowledge_entries(id, topic, statement, status, verification_basis, confidence_basis_points, source_conversation_id, source_turn_id, created_at_ms, updated_at_ms)
VALUES
  ('knowledge-candidate', '主题', '候选事实', 'candidate', 'unverified', 7000, 'conversation-1', 'turn-1', 1, 1),
  ('knowledge-verified', '主题', '已验证事实', 'verified', 'user_confirmed', 9000, 'conversation-1', 'turn-1', 1, 1);
`)
	if err != nil {
		t.Fatalf("seed postgres memory rows: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	summary, err := store.SummaryContext(ctx)
	if err != nil {
		t.Fatalf("SummaryContext: %v", err)
	}
	service, err := NewMemoryServiceFromStore(store)
	if err != nil {
		t.Fatalf("NewMemoryServiceFromStore: %v", err)
	}
	serviceSummary, err := service.SummaryContext(ctx)
	if err != nil {
		t.Fatalf("service SummaryContext: %v", err)
	}
	if summary.SchemaVersion != pgstore.CurrentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", summary.SchemaVersion, pgstore.CurrentSchemaVersion)
	}
	if summary.Conversations != 1 || summary.ActiveGlobalMemories != 1 || summary.NeedsReviewMemories != 1 || summary.PendingExtractionTurns != 1 || summary.CandidateKnowledge != 1 || summary.VerifiedKnowledge != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if serviceSummary != summary {
		t.Fatalf("service summary = %#v, want %#v", serviceSummary, summary)
	}
	if !summary.ReadOnly {
		t.Fatalf("summary.ReadOnly = false, want true")
	}
}

func TestPostgresStoreSummaryHonorsCanceledContext(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	before := pool.Stats().AcquiredConns
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.SummaryContext(canceled); err == nil {
		t.Fatal("SummaryContext() error = nil, want canceled context error")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pool.Stats().AcquiredConns == before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("acquired connections = %d, want %d", pool.Stats().AcquiredConns, before)
}

func TestPostgresConversationFailedTurnPreservesUserOnly(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversationContext: %v", err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurnContext: %v", err)
	}
	if err := store.FailTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "PROVIDER_FAILED", "provider unavailable", true); err != nil {
		t.Fatalf("FailTurnContext: %v", err)
	}
	reloaded, err := store.LoadConversationContext(ctx, bootstrap.Conversation.ID)
	if err != nil {
		t.Fatalf("LoadConversationContext: %v", err)
	}
	if len(reloaded.Messages) != 1 || reloaded.Messages[0].Role != "user" || reloaded.Messages[0].Content != "你好" {
		t.Fatalf("messages = %#v, want one user message", reloaded.Messages)
	}
	var status string
	var assistantCount int
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM conversation_turns WHERE id = $1", turn.ID).Scan(&status); err != nil {
		t.Fatalf("query turn status: %v", err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM conversation_messages WHERE turn_id = $1 AND role = 'assistant'", turn.ID).Scan(&assistantCount); err != nil {
		t.Fatalf("query assistant count: %v", err)
	}
	if status != "failed" || assistantCount != 0 {
		t.Fatalf("status = %q, assistantCount = %d", status, assistantCount)
	}
}

func TestPostgresConversationInterruptedTurnWithoutPrefixPreservesUserOnly(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-interrupt-empty")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversationContext: %v", err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "先停一下")
	if err != nil {
		t.Fatalf("BeginTurnContext: %v", err)
	}
	assistant, err := store.InterruptTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "")
	if err != nil {
		t.Fatalf("InterruptTurnContext: %v", err)
	}
	if assistant != nil {
		t.Fatalf("assistant = %#v, want nil", assistant)
	}
	assertInterruptedTurn(t, ctx, pool.Raw(), bootstrap.Conversation.ID, turn.ID, []string{"先停一下"})
}

func TestPostgresConversationInterruptedTurnPersistsPublishedPrefix(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-interrupt-prefix")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversationContext: %v", err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "慢慢说")
	if err != nil {
		t.Fatalf("BeginTurnContext: %v", err)
	}
	const prefix = "第一拍\n第二拍"
	assistant, err := store.InterruptTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, prefix)
	if err != nil {
		t.Fatalf("InterruptTurnContext: %v", err)
	}
	if assistant == nil || assistant.Role != "assistant" || assistant.Content != prefix || assistant.Sequence != 2 {
		t.Fatalf("assistant = %#v", assistant)
	}
	assertInterruptedTurn(t, ctx, pool.Raw(), bootstrap.Conversation.ID, turn.ID, []string{"慢慢说", prefix})
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "不应写入"); err == nil {
		t.Fatal("CompleteTurnContext() after interrupt error = nil")
	}
}

func TestPostgresConversationInterruptRollbackOnAssistantConflict(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-interrupt-rollback")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversationContext: %v", err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "触发回滚")
	if err != nil {
		t.Fatalf("BeginTurnContext: %v", err)
	}
	if _, err := pool.Raw().Exec(ctx, "INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms) VALUES ('preexisting-assistant', $1, $2, 2, 'assistant', '冲突', 1)", bootstrap.Conversation.ID, turn.ID); err != nil {
		t.Fatalf("seed conflicting assistant: %v", err)
	}
	if _, err := store.InterruptTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "第一拍"); err == nil {
		t.Fatal("InterruptTurnContext() error = nil, want unique assistant conflict")
	}
	var status, extractionState string
	if err := pool.Raw().QueryRow(ctx, "SELECT status, extraction_state FROM conversation_turns WHERE id = $1", turn.ID).Scan(&status, &extractionState); err != nil {
		t.Fatalf("query turn: %v", err)
	}
	if status != "interpreting" || extractionState != "ineligible" {
		t.Fatalf("turn = (%q, %q), want transaction rollback", status, extractionState)
	}
}

func assertInterruptedTurn(t *testing.T, ctx context.Context, pool *pgxpool.Pool, conversationID, turnID string, wantContents []string) {
	t.Helper()
	var status, extractionState string
	var errorCode, errorMessage *string
	var errorRetryable *bool
	if err := pool.QueryRow(ctx, "SELECT status, extraction_state, error_code, error_message, error_retryable FROM conversation_turns WHERE id = $1", turnID).Scan(&status, &extractionState, &errorCode, &errorMessage, &errorRetryable); err != nil {
		t.Fatalf("query turn: %v", err)
	}
	if status != "interrupted" || extractionState != "ineligible" || errorCode != nil || errorMessage != nil || errorRetryable != nil {
		t.Fatalf("turn = (%q, %q, %#v, %#v, %#v)", status, extractionState, errorCode, errorMessage, errorRetryable)
	}
	rows, err := pool.Query(ctx, "SELECT content FROM conversation_messages WHERE conversation_id = $1 ORDER BY sequence", conversationID)
	if err != nil {
		t.Fatalf("query messages: %v", err)
	}
	defer rows.Close()
	var contents []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			t.Fatalf("scan message: %v", err)
		}
		contents = append(contents, content)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate messages: %v", err)
	}
	if !slices.Equal(contents, wantContents) {
		t.Fatalf("contents = %#v, want %#v", contents, wantContents)
	}
}

func TestPostgresConversationConcurrentSequencesAreUnique(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversationContext: %v", err)
	}
	const callers = 8
	errCh := make(chan error, callers)
	var wg sync.WaitGroup
	for index := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, fmt.Sprintf("message-%d", index))
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("BeginTurnContext: %v", err)
		}
	}
	var turnCount, turnSequences, messageCount, messageSequences int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*), count(DISTINCT sequence) FROM conversation_turns WHERE conversation_id = $1", bootstrap.Conversation.ID).Scan(&turnCount, &turnSequences); err != nil {
		t.Fatalf("query turn sequences: %v", err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*), count(DISTINCT sequence) FROM conversation_messages WHERE conversation_id = $1", bootstrap.Conversation.ID).Scan(&messageCount, &messageSequences); err != nil {
		t.Fatalf("query message sequences: %v", err)
	}
	if turnCount != callers || turnSequences != callers || messageCount != callers || messageSequences != callers {
		t.Fatalf("turns=(%d,%d) messages=(%d,%d), want all %d", turnCount, turnSequences, messageCount, messageSequences, callers)
	}
}

func TestPostgresConversationConcurrentOpenReusesCharacterConversation(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	const callers = 6
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-1")
			if err == nil {
				ids <- bootstrap.Conversation.ID
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("OpenOrCreateCharacterConversationContext: %v", err)
		}
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("conversation id = %q, want %q", id, first)
		}
	}
	var count int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM conversations WHERE character_id = 'character-1'").Scan(&count); err != nil {
		t.Fatalf("query conversation count: %v", err)
	}
	if count != 1 {
		t.Fatalf("conversation count = %d, want 1", count)
	}
}

func TestPostgresRuntimeLedgerAndWindowRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversationContext: %v", err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "你好")
	if err != nil {
		t.Fatalf("BeginTurnContext: %v", err)
	}
	const eventCount = 6
	var wg sync.WaitGroup
	errs := make(chan error, eventCount)
	for index := range eventCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.AppendTurnRuntimeEventContext(ctx, TurnRuntimeEventInput{ConversationID: bootstrap.Conversation.ID, TurnID: turn.ID, EventType: "model", MetadataJSON: fmt.Sprintf(`{"index":%d}`, index)})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("AppendTurnRuntimeEventContext: %v", err)
		}
	}
	events, err := store.ListTurnRuntimeEventsContext(ctx, bootstrap.Conversation.ID, turn.ID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEventsContext: %v", err)
	}
	if len(events) != eventCount {
		t.Fatalf("events = %d, want %d", len(events), eventCount)
	}
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			t.Fatalf("event[%d].Sequence = %d", index, event.Sequence)
		}
	}
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	hashC := strings.Repeat("c", 64)
	continuation, err := store.SaveLaneContinuationContext(ctx, LaneContinuationRecord{ConversationID: bootstrap.Conversation.ID, Lane: PromptLaneRespond, PreviousResponseID: "response-1", RequestShapeHash: hashA, InputPrefixHash: hashB, ResponseItemHash: hashC, WindowRevision: 1})
	if err != nil {
		t.Fatalf("SaveLaneContinuationContext: %v", err)
	}
	loadedContinuation, ok, err := store.LoadLaneContinuationContext(ctx, bootstrap.Conversation.ID, PromptLaneRespond)
	if err != nil || !ok || loadedContinuation != continuation {
		t.Fatalf("LoadLaneContinuationContext = (%#v, %v, %v), want %#v", loadedContinuation, ok, err, continuation)
	}
	observed := uint64(100)
	estimated := uint64(120)
	previous := "window-0"
	window, err := store.SaveContextWindowContext(ctx, ContextWindowRecord{ConversationID: bootstrap.Conversation.ID, Lane: PromptLaneRespond, WindowNumber: 1, FirstWindowID: "window-first", PreviousWindowID: &previous, WindowID: "window-1", ObservedPrefillTokens: &observed, EstimatedPrefillTokens: &estimated, LastTrigger: "created", FailureCount: 0, PromptWindowRevision: 1})
	if err != nil {
		t.Fatalf("SaveContextWindowContext: %v", err)
	}
	loadedWindow, ok, err := store.LoadContextWindowContext(ctx, bootstrap.Conversation.ID, PromptLaneRespond)
	if err != nil || !ok {
		t.Fatalf("LoadContextWindowContext = (%#v, %v, %v)", loadedWindow, ok, err)
	}
	if loadedWindow.WindowID != window.WindowID || loadedWindow.PreviousWindowID == nil || *loadedWindow.PreviousWindowID != previous || loadedWindow.ObservedPrefillTokens == nil || *loadedWindow.ObservedPrefillTokens != observed || loadedWindow.EstimatedPrefillTokens == nil || *loadedWindow.EstimatedPrefillTokens != estimated {
		t.Fatalf("loaded window = %#v, want %#v", loadedWindow, window)
	}
	if err := store.ClearLaneContinuationContext(ctx, bootstrap.Conversation.ID, PromptLaneRespond); err != nil {
		t.Fatalf("ClearLaneContinuationContext: %v", err)
	}
	if _, ok, err := store.LoadLaneContinuationContext(ctx, bootstrap.Conversation.ID, PromptLaneRespond); err != nil || ok {
		t.Fatalf("LoadLaneContinuationContext after clear = (ok=%v, err=%v)", ok, err)
	}
}

func TestPostgresUsageLedgerPreservesAggregation(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-usage")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversationContext: %v", err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "usage test")
	if err != nil {
		t.Fatalf("BeginTurnContext: %v", err)
	}
	firstCache := uint64(400)
	secondCache := uint64(600)
	appendModelUsageEvent(t, store, bootstrap.Conversation.ID, turn.ID, "respond", 1000, 120, &firstCache)
	appendModelUsageEvent(t, store, bootstrap.Conversation.ID, turn.ID, "respond", 1500, 80, &secondCache)
	appendTerminalEvent(t, store, bootstrap.Conversation.ID, turn.ID, "completed")
	report, err := store.AggregateTokenUsageContext(ctx, 0)
	if err != nil {
		t.Fatalf("AggregateTokenUsageContext: %v", err)
	}
	if report.TurnCount != 1 || len(report.Turns) != 1 || report.Turns[0].CharacterID != "character-usage" || report.Turns[0].Status != "completed" {
		t.Fatalf("report = %#v", report)
	}
	respond := findLane(t, report.Turns[0].Lanes, "respond")
	if respond.InputTokens != 2500 || respond.OutputTokens != 200 || respond.CachedInputTokens != 1000 || respond.CachedObservedInputTokens != 2500 || respond.CallCount != 2 {
		t.Fatalf("respond lane = %#v", respond)
	}
	if overall := findLane(t, report.Overall, "respond"); overall != respond {
		t.Fatalf("overall = %#v, want %#v", overall, respond)
	}
}

func TestPostgresUsageLedgerPreservesCrossConversationFailureAndTruncation(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	for index, characterID := range []string{"character-usage-a", "character-usage-b"} {
		bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, characterID)
		if err != nil {
			t.Fatal(err)
		}
		turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, fmt.Sprintf("usage %d", index))
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			cache := uint64(40)
			appendModelUsageEvent(t, store, bootstrap.Conversation.ID, turn.ID, "respond", 100, 20, &cache)
			appendTerminalEvent(t, store, bootstrap.Conversation.ID, turn.ID, "completed")
		} else {
			appendModelUsageEvent(t, store, bootstrap.Conversation.ID, turn.ID, "respond", 200, 30, nil)
			appendTerminalEvent(t, store, bootstrap.Conversation.ID, turn.ID, "failed")
		}
	}
	report, err := store.AggregateTokenUsageContext(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if report.TurnCount != 2 || len(report.Turns) != 1 || !report.Truncated {
		t.Fatalf("report = %#v, want 2 total, 1 detail, truncated", report)
	}
	overall := findLane(t, report.Overall, "respond")
	if overall.InputTokens != 300 || overall.OutputTokens != 50 || overall.CachedInputTokens != 40 || overall.CachedObservedInputTokens != 100 || overall.CallCount != 2 {
		t.Fatalf("overall = %#v", overall)
	}
	if report.Turns[0].Status != "failed" || report.Turns[0].CharacterID != "character-usage-b" {
		t.Fatalf("latest turn = %#v", report.Turns[0])
	}
}

func TestPostgresPersonalMemoryLifecycleQueuesDeterministicOutbox(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-memory")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "我喜欢安静")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "我记住了"); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreatePersonalMemoryContext(ctx, "preference", MemoryScope{Type: "global"}, "喜欢安静", 9000)
	if err != nil {
		t.Fatalf("CreatePersonalMemoryContext: %v", err)
	}
	assertPostgresEmbeddingOutbox(t, ctx, pool, vectorindex.ItemKindPersonalMemory, created.ID, "喜欢安静")
	revised, err := store.RevisePersonalMemoryContext(ctx, created.ID, "更喜欢安静的环境", 9200)
	if err != nil {
		t.Fatalf("RevisePersonalMemoryContext: %v", err)
	}
	if revised.SupersedesID == nil || *revised.SupersedesID != created.ID {
		t.Fatalf("revised = %#v", revised)
	}
	assertPostgresEmbeddingOutbox(t, ctx, pool, vectorindex.ItemKindPersonalMemory, revised.ID, "更喜欢安静的环境")
	var oldStatus string
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM personal_memories WHERE id = $1", created.ID).Scan(&oldStatus); err != nil || oldStatus != "superseded" {
		t.Fatalf("old status = %q, err=%v", oldStatus, err)
	}
	if err := store.TombstonePersonalMemoryContext(ctx, revised.ID); err != nil {
		t.Fatalf("TombstonePersonalMemoryContext: %v", err)
	}
	var revisedStatus string
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM personal_memories WHERE id = $1", revised.ID).Scan(&revisedStatus); err != nil || revisedStatus != "tombstone" {
		t.Fatalf("revised status = %q, err=%v", revisedStatus, err)
	}
	legacy, err := store.CreatePersonalMemoryContext(ctx, "relationship", MemoryScope{Type: "unassigned_legacy"}, "旧关系记忆", 7000)
	if err != nil {
		t.Fatalf("CreatePersonalMemoryContext legacy: %v", err)
	}
	var legacyJobs int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM memory_embedding_jobs WHERE item_id = $1", legacy.ID).Scan(&legacyJobs); err != nil || legacyJobs != 0 {
		t.Fatalf("legacy jobs = %d, err=%v", legacyJobs, err)
	}
	assigned, err := store.AssignLegacyRelationshipContext(ctx, legacy.ID, "character-memory")
	if err != nil {
		t.Fatalf("AssignLegacyRelationshipContext: %v", err)
	}
	assertPostgresEmbeddingOutbox(t, ctx, pool, vectorindex.ItemKindPersonalMemory, assigned.ID, "旧关系记忆")
	catalog, err := store.PersonalMemoryCatalogContext(ctx, "character-memory")
	if err != nil {
		t.Fatalf("PersonalMemoryCatalogContext: %v", err)
	}
	if len(catalog.Global) != 0 || len(catalog.Character) != 1 || catalog.Character[0].ID != assigned.ID || len(catalog.NeedsReview) != 0 {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestPostgresPersonalMemoryRollsBackWhenOutboxWriteFails(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-rollback")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "source reply"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Raw().Exec(ctx, "DROP TABLE memory_embedding_jobs"); err != nil {
		t.Fatalf("drop outbox table: %v", err)
	}
	if _, err := store.CreatePersonalMemoryContext(ctx, "preference", MemoryScope{Type: "global"}, "must rollback", 9000); err == nil {
		t.Fatal("CreatePersonalMemoryContext error = nil, want outbox failure")
	}
	var memories, items int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM personal_memories WHERE content = 'must rollback'").Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM memory_embedding_items").Scan(&items); err != nil {
		t.Fatal(err)
	}
	if memories != 0 || items != 0 {
		t.Fatalf("memories=%d items=%d, want zero after rollback", memories, items)
	}
}

func TestPostgresKnowledgeLifecyclePreservesSourcesAndOutbox(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-knowledge")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "source reply"); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Raw().Exec(ctx, `
INSERT INTO knowledge_entries(id, topic, statement, status, verification_basis, confidence_basis_points, source_conversation_id, source_turn_id, created_at_ms, updated_at_ms)
VALUES
  ('candidate-user', '主题一', '候选事实', 'candidate', 'unverified', 8000, $1, $2, 1, 1),
	('candidate-web', '主题二', '网页事实', 'candidate', 'unverified', 7000, $1, $2, 2, 2)`, bootstrap.Conversation.ID, turn.ID)
	if err != nil {
		t.Fatalf("seed knowledge: %v", err)
	}
	if _, err := pool.Raw().Exec(ctx, "INSERT INTO knowledge_sources(knowledge_id, source_id, title, url, snippet, rank, fetched_at_ms) VALUES ('candidate-web', 'source-1', '来源标题', 'https://example.test/source', '来源摘要', 1, 2)"); err != nil {
		t.Fatalf("seed knowledge source: %v", err)
	}
	confirmed, err := store.ConfirmKnowledgeCandidateContext(ctx, "candidate-user")
	if err != nil {
		t.Fatalf("ConfirmKnowledgeCandidateContext: %v", err)
	}
	if confirmed.Status != "verified" || confirmed.VerificationBasis != "user_confirmed" || len(confirmed.Sources) != 0 {
		t.Fatalf("confirmed = %#v", confirmed)
	}
	assertPostgresEmbeddingOutbox(t, ctx, pool, vectorindex.ItemKindKnowledge, confirmed.ID, "主题一\n候选事实")
	if _, err := store.ConfirmKnowledgeCandidateContext(ctx, "candidate-web"); err == nil {
		t.Fatal("ConfirmKnowledgeCandidateContext sourced candidate error = nil")
	}
	catalog, err := store.KnowledgeCatalogContext(ctx)
	if err != nil {
		t.Fatalf("KnowledgeCatalogContext: %v", err)
	}
	if len(catalog.Candidates) != 1 || catalog.Candidates[0].ID != "candidate-web" || len(catalog.Candidates[0].Sources) != 1 || len(catalog.Verified) != 1 || catalog.Verified[0].ID != confirmed.ID {
		t.Fatalf("catalog = %#v", catalog)
	}
	if err := store.TombstoneKnowledgeContext(ctx, "candidate-web"); err != nil {
		t.Fatalf("TombstoneKnowledgeContext: %v", err)
	}
	var status string
	var sourceCount int
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM knowledge_entries WHERE id = 'candidate-web'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM knowledge_sources WHERE knowledge_id = 'candidate-web'").Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if status != "tombstone" || sourceCount != 1 {
		t.Fatalf("status=%q sourceCount=%d", status, sourceCount)
	}
}

func TestPostgresKnowledgeConfirmationRollsBackWhenOutboxWriteFails(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-knowledge-rollback")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "source reply"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Raw().Exec(ctx, "INSERT INTO knowledge_entries(id, topic, statement, status, verification_basis, confidence_basis_points, source_conversation_id, source_turn_id, created_at_ms, updated_at_ms) VALUES ('candidate-rollback', '主题', 'must rollback', 'candidate', 'unverified', 8000, $1, $2, 1, 1)", bootstrap.Conversation.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Raw().Exec(ctx, "DROP TABLE memory_embedding_jobs"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmKnowledgeCandidateContext(ctx, "candidate-rollback"); err == nil {
		t.Fatal("ConfirmKnowledgeCandidateContext error = nil, want outbox failure")
	}
	var status, basis string
	var items int
	if err := pool.Raw().QueryRow(ctx, "SELECT status, verification_basis FROM knowledge_entries WHERE id = 'candidate-rollback'").Scan(&status, &basis); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM memory_embedding_items").Scan(&items); err != nil {
		t.Fatal(err)
	}
	if status != "candidate" || basis != "unverified" || items != 0 {
		t.Fatalf("status=%q basis=%q items=%d", status, basis, items)
	}
}

func TestPostgresPromptWindowCommitPreservesRevisionAndCutoff(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-compaction")
	if err != nil {
		t.Fatal(err)
	}
	for index := range 2 {
		turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, fmt.Sprintf("user-%d", index))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, fmt.Sprintf("assistant-%d", index)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.CommitPromptWindowContext(ctx, bootstrap.Conversation.ID, 1, "  已压缩摘要  ")
	if err != nil {
		t.Fatalf("CommitPromptWindowContext: %v", err)
	}
	if result.WindowRevision != 2 {
		t.Fatalf("result = %#v", result)
	}
	reloaded, err := store.LoadConversationContext(ctx, bootstrap.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PromptWindow.Revision != 2 || reloaded.PromptWindow.Summary == nil || *reloaded.PromptWindow.Summary != "已压缩摘要" || reloaded.PromptWindow.CutoffMessageSequence != 4 {
		t.Fatalf("prompt window = %#v", reloaded.PromptWindow)
	}
	if _, err := store.CommitPromptWindowContext(ctx, bootstrap.Conversation.ID, 1, "stale summary"); err == nil {
		t.Fatal("stale CommitPromptWindowContext error = nil")
	}
	afterStale, err := store.LoadConversationContext(ctx, bootstrap.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterStale.PromptWindow.Revision != 2 || afterStale.PromptWindow.Summary == nil || *afterStale.PromptWindow.Summary != "已压缩摘要" {
		t.Fatalf("prompt window after stale write = %#v", afterStale.PromptWindow)
	}
}

func TestPostgresCommitMemoryMutationsCommitsRowsAndOutboxAtomically(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID, batchID := seedPostgresRunningExtractionBatch(t, ctx, pool, store, "character-mutation-success")
	results, err := store.CommitMemoryMutationsContext(ctx, batchID, "character-mutation-success", nil, []MemoryMutation{
		{Operation: "create", Kind: "preference", Scope: MemoryScope{Type: "global"}, Content: "喜欢爵士乐", ConfidenceBasisPoints: 9000},
		{Operation: "create", Kind: "relationship", Scope: MemoryScope{Type: "character", CharacterID: "character-mutation-success"}, Content: "愿意分享近况", ConfidenceBasisPoints: 8500},
	})
	if err != nil {
		t.Fatalf("CommitMemoryMutationsContext: %v", err)
	}
	if len(results) != 2 || results[0].Status != "applied" || results[1].Status != "applied" {
		t.Fatalf("results = %#v", results)
	}
	assertPostgresEmbeddingOutbox(t, ctx, pool, vectorindex.ItemKindPersonalMemory, results[0].MemoryID, "喜欢爵士乐")
	assertPostgresEmbeddingOutbox(t, ctx, pool, vectorindex.ItemKindPersonalMemory, results[1].MemoryID, "愿意分享近况")
	var batchStatus, extractionState string
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM extraction_batches WHERE id = $1 AND conversation_id = $2", batchID, conversationID).Scan(&batchStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT extraction_state FROM conversation_turns WHERE id = $1", turnID).Scan(&extractionState); err != nil {
		t.Fatal(err)
	}
	if batchStatus != "succeeded" || extractionState != "processed" {
		t.Fatalf("batchStatus=%q extractionState=%q", batchStatus, extractionState)
	}
}

func TestPostgresCommitMemoryMutationsRollsBackEarlierMutationOnLaterFailure(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	_, turnID, batchID := seedPostgresRunningExtractionBatch(t, ctx, pool, store, "character-mutation-rollback")
	_, err = store.CommitMemoryMutationsContext(ctx, batchID, "character-mutation-rollback", nil, []MemoryMutation{
		{Operation: "create", Kind: "preference", Scope: MemoryScope{Type: "global"}, Content: "must rollback", ConfidenceBasisPoints: 9000},
		{Operation: "supersede", MemoryID: "not-allowed-memory", Kind: "profile", Scope: MemoryScope{Type: "global"}, Content: "later failure", ConfidenceBasisPoints: 8000},
	})
	if err == nil || !strings.Contains(err.Error(), "not provided to the batch") {
		t.Fatalf("CommitMemoryMutationsContext error = %v", err)
	}
	var memories, items, jobs int
	var batchStatus, extractionState string
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM personal_memories WHERE content = 'must rollback'").Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM memory_embedding_items").Scan(&items); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM memory_embedding_jobs").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM extraction_batches WHERE id = $1", batchID).Scan(&batchStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT extraction_state FROM conversation_turns WHERE id = $1", turnID).Scan(&extractionState); err != nil {
		t.Fatal(err)
	}
	if memories != 0 || items != 0 || jobs != 0 || batchStatus != "running" || extractionState != "claimed" {
		t.Fatalf("memories=%d items=%d jobs=%d batch=%q turn=%q", memories, items, jobs, batchStatus, extractionState)
	}
}

func TestPostgresCommitMemoryMutationsPreservesNoChangeAndSupersedeSemantics(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	_, _, initialBatchID := seedPostgresRunningExtractionBatch(t, ctx, pool, store, "character-mutation-parity")
	initialResults, err := store.CommitMemoryMutationsContext(ctx, initialBatchID, "character-mutation-parity", nil, []MemoryMutation{{Operation: "create", Kind: "preference", Scope: MemoryScope{Type: "global"}, Content: "喜欢安静", ConfidenceBasisPoints: 9000}})
	if err != nil {
		t.Fatal(err)
	}
	if len(initialResults) != 1 || initialResults[0].Status != "applied" {
		t.Fatalf("initial results = %#v", initialResults)
	}
	initialID := initialResults[0].MemoryID
	_, _, batchID := seedPostgresRunningExtractionBatch(t, ctx, pool, store, "character-mutation-parity")
	results, err := store.CommitMemoryMutationsContext(ctx, batchID, "character-mutation-parity", []string{initialID}, []MemoryMutation{
		{Operation: "create", Kind: "preference", Scope: MemoryScope{Type: "global"}, Content: "喜欢安静", ConfidenceBasisPoints: 9000},
		{Operation: "supersede", MemoryID: initialID, Kind: "preference", Scope: MemoryScope{Type: "global"}, Content: "喜欢清晨散步", ConfidenceBasisPoints: 9300},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Status != "no_change" || results[0].ExistingMemoryID != initialID || results[1].Status != "applied" || results[1].MemoryID == "" {
		t.Fatalf("results = %#v", results)
	}
	var oldStatus, newStatus, supersedesID string
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM personal_memories WHERE id = $1", initialID).Scan(&oldStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT status, supersedes_id FROM personal_memories WHERE id = $1", results[1].MemoryID).Scan(&newStatus, &supersedesID); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "superseded" || newStatus != "active" || supersedesID != initialID {
		t.Fatalf("old=%q new=%q supersedes=%q", oldStatus, newStatus, supersedesID)
	}
	assertPostgresEmbeddingOutbox(t, ctx, pool, vectorindex.ItemKindPersonalMemory, results[1].MemoryID, "喜欢清晨散步")
}

func seedPostgresRunningExtractionBatch(t *testing.T, ctx context.Context, pool *pgstore.Pool, store *Store, characterID string) (string, string, string) {
	t.Helper()
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, characterID)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "batch source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "batch reply"); err != nil {
		t.Fatal(err)
	}
	batchID := newID()
	_, err = pool.Raw().Exec(ctx, `
INSERT INTO extraction_batches(id, conversation_id, character_id, status, first_turn_sequence, last_turn_sequence, lease_owner, lease_expires_at_ms, attempt_count, created_at_ms, updated_at_ms)
VALUES ($1, $2, $3, 'running', 1, 1, $4, 9999999999999, 1, 1, 1)`, batchID, bootstrap.Conversation.ID, characterID, store.workerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Raw().Exec(ctx, "INSERT INTO extraction_batch_turns(batch_id, turn_id, turn_sequence) VALUES ($1, $2, 1)", batchID, turn.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Raw().Exec(ctx, "UPDATE conversation_turns SET extraction_state = 'claimed' WHERE id = $1", turn.ID); err != nil {
		t.Fatal(err)
	}
	return bootstrap.Conversation.ID, turn.ID, batchID
}

func TestPostgresExtractionLeasePreventsDuplicateClaimAndRecoversExpiredOwner(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	first, err := newStoreFromPoolWithLease(pool, "worker-first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newStoreFromPoolWithLease(pool, "worker-second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := first.OpenOrCreateCharacterConversationContext(ctx, "character-lease")
	if err != nil {
		t.Fatal(err)
	}
	for index := range 3 {
		turn, err := first.BeginTurnContext(ctx, bootstrap.Conversation.ID, fmt.Sprintf("user-%d", index))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := first.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, fmt.Sprintf("assistant-%d", index)); err != nil {
			t.Fatal(err)
		}
	}
	type claimResult struct {
		batch *ExtractionBatchInput
		err   error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	for _, store := range []*Store{first, second} {
		go func() {
			<-start
			batch, err := store.ClaimExtractionBatchContext(ctx, bootstrap.Conversation.ID, 3)
			results <- claimResult{batch: batch, err: err}
		}()
	}
	close(start)
	firstResult := <-results
	secondResult := <-results
	if firstResult.err != nil || secondResult.err != nil {
		t.Fatalf("claim errors = %v, %v", firstResult.err, secondResult.err)
	}
	claimed := firstResult.batch
	if claimed == nil {
		claimed = secondResult.batch
	}
	if claimed == nil || (firstResult.batch != nil && secondResult.batch != nil) || len(claimed.Turns) != 3 {
		t.Fatalf("claim results = %#v, %#v", firstResult.batch, secondResult.batch)
	}
	var owner string
	var attempts int
	if err := pool.Raw().QueryRow(ctx, "SELECT lease_owner, attempt_count FROM extraction_batches WHERE id = $1", claimed.BatchID).Scan(&owner, &attempts); err != nil {
		t.Fatal(err)
	}
	ownerStore := first
	otherStore := second
	if owner == second.workerID {
		ownerStore, otherStore = second, first
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if err := otherStore.CompleteExtractionBatchContext(ctx, claimed.BatchID); err == nil {
		t.Fatal("wrong owner completion error = nil")
	}
	if _, err := pool.Raw().Exec(ctx, "UPDATE extraction_batches SET lease_expires_at_ms = $2 WHERE id = $1", claimed.BatchID, nowUnixMS()-1); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := otherStore.ClaimExtractionBatchContext(ctx, bootstrap.Conversation.ID, 3)
	if err != nil {
		t.Fatalf("expired reclaim: %v", err)
	}
	if reclaimed == nil || reclaimed.BatchID != claimed.BatchID || len(reclaimed.Turns) != 3 {
		t.Fatalf("reclaimed = %#v, want batch %s", reclaimed, claimed.BatchID)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT lease_owner, attempt_count FROM extraction_batches WHERE id = $1", claimed.BatchID).Scan(&owner, &attempts); err != nil {
		t.Fatal(err)
	}
	if owner != otherStore.workerID || attempts != 2 {
		t.Fatalf("owner=%q attempts=%d", owner, attempts)
	}
	if err := ownerStore.FailExtractionBatchContext(ctx, claimed.BatchID, "WRONG_OWNER", "must fail", false); err == nil {
		t.Fatal("stale owner failure update error = nil")
	}
	if err := otherStore.CompleteExtractionBatchContext(ctx, claimed.BatchID); err != nil {
		t.Fatalf("reclaimed completion: %v", err)
	}
	var processed int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM conversation_turns WHERE conversation_id = $1 AND extraction_state = 'processed'", bootstrap.Conversation.ID).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if processed != 3 {
		t.Fatalf("processed = %d, want 3", processed)
	}
}

func TestPostgresExtractionFailedCatalogAndRetryReleaseTurns(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := newStoreFromPoolWithLease(pool, "worker-retry", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-retry")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "retry source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "retry reply"); err != nil {
		t.Fatal(err)
	}
	batch, err := store.ClaimExtractionBatchContext(ctx, bootstrap.Conversation.ID, 1)
	if err != nil || batch == nil {
		t.Fatalf("claim = %#v, %v", batch, err)
	}
	if err := store.FailExtractionBatchContext(ctx, batch.BatchID, "MODEL_FAILED", "provider failed", true); err != nil {
		t.Fatal(err)
	}
	catalog, err := store.ExtractionBatchCatalogContext(ctx, "character-retry")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Failed) != 1 || catalog.Failed[0].Error == nil || catalog.Failed[0].Error.Code != "MODEL_FAILED" || !catalog.Failed[0].Error.Retryable {
		t.Fatalf("catalog = %#v", catalog)
	}
	if err := store.RetryExtractionBatchContext(ctx, batch.BatchID); err != nil {
		t.Fatal(err)
	}
	var batchStatus, turnState string
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM extraction_batches WHERE id = $1", batch.BatchID).Scan(&batchStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT extraction_state FROM conversation_turns WHERE id = $1", turn.ID).Scan(&turnState); err != nil {
		t.Fatal(err)
	}
	if batchStatus != "cancelled" || turnState != "pending" {
		t.Fatalf("batch=%q turn=%q", batchStatus, turnState)
	}
	reclaimed, err := store.ClaimExtractionBatchContext(ctx, bootstrap.Conversation.ID, 1)
	if err != nil || reclaimed == nil || reclaimed.BatchID == batch.BatchID {
		t.Fatalf("reclaimed = %#v, %v", reclaimed, err)
	}
}

func TestPostgresKnowledgeIngestWorkersClaimDisjointJobs(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	first, err := newStoreFromPoolWithLease(pool, "ingest-first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newStoreFromPoolWithLease(pool, "ingest-second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := first.OpenOrCreateCharacterConversationContext(ctx, "character-ingest")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := first.BeginTurnContext(ctx, bootstrap.Conversation.ID, "ingest source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "ingest reply"); err != nil {
		t.Fatal(err)
	}
	snapshots := make([]KnowledgeIngestSnapshot, 0, 6)
	for index := range 6 {
		snapshots = append(snapshots, KnowledgeIngestSnapshot{ConversationID: bootstrap.Conversation.ID, TurnID: turn.ID, Query: fmt.Sprintf("query-%d", index), Title: fmt.Sprintf("topic-%d", index), URL: fmt.Sprintf("https://example.test/%d", index), Snippet: fmt.Sprintf("这是第 %d 条足够长的知识摘要内容。", index), Rank: uint8(index%5 + 1), FetchedAtUnixMS: int64(index + 1)})
	}
	if err := first.EnqueueKnowledgeIngestSnapshotsContext(ctx, snapshots); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	counts := make(chan int, 2)
	errs := make(chan error, 2)
	for _, store := range []*Store{first, second} {
		go func() {
			<-start
			count, err := store.ProcessKnowledgeIngestJobsContext(ctx, 3)
			counts <- count
			errs <- err
		}()
	}
	close(start)
	written := <-counts + <-counts
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if written != 6 {
		t.Fatalf("written = %d, want 6", written)
	}
	var succeeded, knowledge, items, jobs int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM knowledge_ingest_jobs WHERE status = 'succeeded'").Scan(&succeeded); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM knowledge_entries WHERE verification_basis = 'retrieval_ingest'").Scan(&knowledge); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM memory_embedding_items WHERE item_kind = 'knowledge'").Scan(&items); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM memory_embedding_jobs WHERE item_kind = 'knowledge'").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if succeeded != 6 || knowledge != 6 || items != 6 || jobs != 6 {
		t.Fatalf("succeeded=%d knowledge=%d items=%d jobs=%d", succeeded, knowledge, items, jobs)
	}
}

func TestPostgresKnowledgeIngestExpiredLeaseReclaimsAndRejectsOldOwner(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	first, err := newStoreFromPoolWithLease(pool, "ingest-owner-first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newStoreFromPoolWithLease(pool, "ingest-owner-second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := first.OpenOrCreateCharacterConversationContext(ctx, "character-ingest-reclaim")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := first.BeginTurnContext(ctx, bootstrap.Conversation.ID, "source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "reply"); err != nil {
		t.Fatal(err)
	}
	if err := first.EnqueueKnowledgeIngestSnapshotsContext(ctx, []KnowledgeIngestSnapshot{{ConversationID: bootstrap.Conversation.ID, TurnID: turn.ID, Query: "query", Title: "topic", URL: "https://example.test", Snippet: "这是一条足够长的待处理知识摘要。", Rank: 1}}); err != nil {
		t.Fatal(err)
	}
	now := nowUnixMS()
	claimed, err := first.claimKnowledgeIngestJobsPostgres(ctx, 1, now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("first claim = %#v, %v", claimed, err)
	}
	if blocked, err := second.claimKnowledgeIngestJobsPostgres(ctx, 1, now+1); err != nil || len(blocked) != 0 {
		t.Fatalf("unexpired second claim = %#v, %v", blocked, err)
	}
	if _, err := pool.Raw().Exec(ctx, "UPDATE knowledge_ingest_jobs SET lease_expires_at_ms = $2 WHERE id = $1", claimed[0].id, now-1); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := second.claimKnowledgeIngestJobsPostgres(ctx, 1, now+2)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].id != claimed[0].id {
		t.Fatalf("reclaim = %#v, %v", reclaimed, err)
	}
	var attempts int
	var owner string
	if err := pool.Raw().QueryRow(ctx, "SELECT attempt_count, lease_owner FROM knowledge_ingest_jobs WHERE id = $1", claimed[0].id).Scan(&attempts, &owner); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || owner != second.workerID {
		t.Fatalf("attempts=%d owner=%q", attempts, owner)
	}
	if err := first.finishKnowledgeIngestJobPostgres(ctx, claimed[0].id, "succeeded", ""); err == nil {
		t.Fatal("old owner completion error = nil")
	}
	if err := second.finishKnowledgeIngestJobPostgres(ctx, claimed[0].id, "dropped", ""); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresKnowledgeIngestDropsStructuralJunkAndValidatesLimits(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-ingest-drop")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "reply"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueKnowledgeIngestSnapshotsContext(ctx, []KnowledgeIngestSnapshot{{ConversationID: bootstrap.Conversation.ID, TurnID: turn.ID, Query: "q", Title: "topic", Snippet: "太短", Rank: 1}}); err != nil {
		t.Fatal(err)
	}
	written, err := store.ProcessKnowledgeIngestJobsContext(ctx, 1)
	if err != nil || written != 0 {
		t.Fatalf("ProcessKnowledgeIngestJobsContext = (%d, %v)", written, err)
	}
	var dropped, knowledge int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM knowledge_ingest_jobs WHERE status = 'dropped'").Scan(&dropped); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM knowledge_entries").Scan(&knowledge); err != nil {
		t.Fatal(err)
	}
	if dropped != 1 || knowledge != 0 {
		t.Fatalf("dropped=%d knowledge=%d", dropped, knowledge)
	}
	if _, err := store.ProcessKnowledgeIngestJobsContext(ctx, 0); err == nil {
		t.Fatal("zero process limit error = nil")
	}
	if err := store.EnqueueKnowledgeIngestSnapshotsContext(ctx, []KnowledgeIngestSnapshot{{ConversationID: bootstrap.Conversation.ID, TurnID: turn.ID, Title: "topic", Snippet: "足够长的内容文本", Rank: 6}}); err == nil {
		t.Fatal("invalid rank error = nil")
	}
	mutations := make([]MemoryMutation, MaxMemoryMutationsPerBatch+1)
	if _, err := store.CommitMemoryMutationsContext(ctx, "batch", "character", nil, mutations); err == nil || !strings.Contains(err.Error(), "mutation limit") {
		t.Fatalf("mutation limit error = %v", err)
	}
}

func TestPostgresEmbeddingLeaseClaimAndConditionalCompletion(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	first, err := newStoreFromPoolWithLease(pool, "embedding-first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newStoreFromPoolWithLease(pool, "embedding-second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := first.OpenOrCreateCharacterConversationContext(ctx, "character-embedding")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := first.BeginTurnContext(ctx, bootstrap.Conversation.ID, "source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "reply"); err != nil {
		t.Fatal(err)
	}
	created, err := first.CreatePersonalMemoryContext(ctx, "preference", MemoryScope{Type: "global"}, "embedding content", 9000)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type embeddingClaim struct {
		store *Store
		jobs  []embeddingJob
		err   error
	}
	claims := make(chan embeddingClaim, 2)
	for _, store := range []*Store{first, second} {
		go func() {
			<-start
			jobs, err := store.claimEmbeddingJobsPostgres(ctx, 1, nowUnixMS())
			claims <- embeddingClaim{store: store, jobs: jobs, err: err}
		}()
	}
	close(start)
	claimA := <-claims
	claimB := <-claims
	if claimA.err != nil || claimB.err != nil {
		t.Fatalf("claim errors = %v, %v", claimA.err, claimB.err)
	}
	ownerClaim := claimA
	otherClaim := claimB
	if len(ownerClaim.jobs) == 0 {
		ownerClaim, otherClaim = otherClaim, ownerClaim
	}
	if len(ownerClaim.jobs) != 1 || len(otherClaim.jobs) != 0 || ownerClaim.jobs[0].ItemID != created.ID {
		t.Fatalf("claims = %#v, %#v", claimA.jobs, claimB.jobs)
	}
	job := ownerClaim.jobs[0]
	content, err := ownerClaim.store.embeddingJobContentPostgres(ctx, job)
	if err != nil || content != "embedding content" {
		t.Fatalf("content = %q, %v", content, err)
	}
	if err := otherClaim.store.finishEmbeddingJobSucceededPostgres(ctx, job, nowUnixMS()); !errors.Is(err, errEmbeddingJobStaleCompletion) {
		t.Fatalf("wrong owner completion error = %v", err)
	}
	if _, err := pool.Raw().Exec(ctx, "UPDATE memory_embedding_jobs SET lease_expires_at_ms = $2 WHERE id = $1", job.ID, nowUnixMS()-1); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := otherClaim.store.claimEmbeddingJobsPostgres(ctx, 1, nowUnixMS())
	if err != nil || len(reclaimed) != 1 || reclaimed[0].ID != job.ID {
		t.Fatalf("reclaimed = %#v, %v", reclaimed, err)
	}
	if err := ownerClaim.store.finishEmbeddingJobFailedPostgres(ctx, job, "STALE", "old owner", false, nowUnixMS()); !errors.Is(err, errEmbeddingJobStaleCompletion) {
		t.Fatalf("old owner failure completion error = %v", err)
	}
	if err := otherClaim.store.finishEmbeddingJobSucceededPostgres(ctx, reclaimed[0], nowUnixMS()); err != nil {
		t.Fatalf("new owner completion: %v", err)
	}
	var itemStatus, jobStatus string
	var embeddedAt *int64
	if err := pool.Raw().QueryRow(ctx, "SELECT status, embedded_at_ms FROM memory_embedding_items WHERE item_id = $1", created.ID).Scan(&itemStatus, &embeddedAt); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM memory_embedding_jobs WHERE id = $1", job.ID).Scan(&jobStatus); err != nil {
		t.Fatal(err)
	}
	if itemStatus != "embedded" || jobStatus != "succeeded" || embeddedAt == nil {
		t.Fatalf("item=%q job=%q embeddedAt=%v", itemStatus, jobStatus, embeddedAt)
	}
}

func TestPostgresEmbeddingStaleContentCannotMarkNewItemEmbedded(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := newStoreFromPoolWithLease(pool, "embedding-stale", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-embedding-stale")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "reply"); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreatePersonalMemoryContext(ctx, "preference", MemoryScope{Type: "global"}, "old content", 9000)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := store.claimEmbeddingJobsPostgres(ctx, 1, nowUnixMS())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim = %#v, %v", jobs, err)
	}
	newHash := semanticContentHash("new content")
	if _, err := pool.Raw().Exec(ctx, "UPDATE memory_embedding_items SET content_hash = $2, status = 'pending', embedded_at_ms = NULL WHERE item_id = $1", created.ID, newHash); err != nil {
		t.Fatal(err)
	}
	if err := store.finishEmbeddingJobSucceededPostgres(ctx, jobs[0], nowUnixMS()); !errors.Is(err, errEmbeddingJobStaleCompletion) {
		t.Fatalf("stale completion error = %v", err)
	}
	var itemHash, itemStatus, jobStatus string
	if err := pool.Raw().QueryRow(ctx, "SELECT content_hash, status FROM memory_embedding_items WHERE item_id = $1", created.ID).Scan(&itemHash, &itemStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM memory_embedding_jobs WHERE id = $1", jobs[0].ID).Scan(&jobStatus); err != nil {
		t.Fatal(err)
	}
	if itemHash != newHash || itemStatus != "pending" || jobStatus != "running" {
		t.Fatalf("itemHash=%q itemStatus=%q jobStatus=%q", itemHash, itemStatus, jobStatus)
	}
}

func TestPostgresTrigramRetrievalPreservesScopeLimitsAndStableOrder(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-search-a")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "search source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "search reply"); err != nil {
		t.Fatal(err)
	}
	global, err := store.CreatePersonalMemoryContext(ctx, "profile", MemoryScope{Type: "global"}, "用户不喜欢太甜的饮料", 9500)
	if err != nil {
		t.Fatal(err)
	}
	relationship, err := store.CreatePersonalMemoryContext(ctx, "relationship", MemoryScope{Type: "character", CharacterID: "character-search-a"}, "亚托莉知道用户喜欢安静陪伴", 9300)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 6 {
		if _, err := store.CreatePersonalMemoryContext(ctx, "preference", MemoryScope{Type: "global"}, fmt.Sprintf("用户喜欢安静音乐候选 %d", index), uint16(9000-index)); err != nil {
			t.Fatal(err)
		}
	}
	knowledge, err := store.InsertVerifiedKnowledgeContext(ctx, "作品发布情报", "某作品将在明年正式发布续作更新", bootstrap.Conversation.ID, turn.ID, 8800, []AssistantSource{{Title: "公告", URL: "https://example.test/news", Snippet: "正式公告摘要", Rank: 1, FetchedAtUnixMS: 10}})
	if err != nil {
		t.Fatal(err)
	}
	phrase, err := store.RetrieveContext(ctx, "character-search-a", "太甜的饮料")
	if err != nil {
		t.Fatal(err)
	}
	if !containsRetrievedPersonalID(phrase.PersonalMemories, global.ID) {
		t.Fatalf("phrase result = %#v", phrase.PersonalMemories)
	}
	if err := store.TombstonePersonalMemoryContext(ctx, global.ID); err != nil {
		t.Fatal(err)
	}
	afterTombstone, err := store.RetrieveContext(ctx, "character-search-a", "太甜的饮料")
	if err != nil {
		t.Fatal(err)
	}
	if containsRetrievedPersonalID(afterTombstone.PersonalMemories, global.ID) {
		t.Fatalf("tombstone remained searchable = %#v", afterTombstone.PersonalMemories)
	}
	current, err := store.RetrieveContext(ctx, "character-search-a", "安静陪伴")
	if err != nil {
		t.Fatal(err)
	}
	if !containsRetrievedPersonalID(current.PersonalMemories, relationship.ID) || current.PersonalMemories[0].Layer == "" {
		t.Fatalf("current relationship = %#v", current.PersonalMemories)
	}
	other, err := store.RetrieveContext(ctx, "character-search-b", "安静陪伴")
	if err != nil {
		t.Fatal(err)
	}
	if containsRetrievedPersonalID(other.PersonalMemories, relationship.ID) {
		t.Fatalf("relationship leaked = %#v", other.PersonalMemories)
	}
	short, err := store.RetrieveContext(ctx, "character-search-a", "饮料")
	if err != nil {
		t.Fatal(err)
	}
	if !short.Empty() || short.SemanticStatus != "unavailable" {
		t.Fatalf("short result = %#v", short)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	shortCanceled, err := store.RetrieveContext(canceled, "character-search-a", "嗯")
	if err != nil || !shortCanceled.Empty() {
		t.Fatalf("canceled short result = %#v, %v", shortCanceled, err)
	}
	preferences, err := store.RetrieveContext(ctx, "character-search-a", "安静音乐候选")
	if err != nil {
		t.Fatal(err)
	}
	preferenceCount := 0
	firstOrder := make([]string, 0)
	for _, item := range preferences.PersonalMemories {
		if item.Kind == "preference" {
			preferenceCount++
			firstOrder = append(firstOrder, item.ID)
		}
	}
	if preferenceCount != maxResultsPerKind {
		t.Fatalf("preference count = %d, result=%#v", preferenceCount, preferences.PersonalMemories)
	}
	repeated, err := store.RetrieveContext(ctx, "character-search-a", "安静音乐候选")
	if err != nil {
		t.Fatal(err)
	}
	secondOrder := make([]string, 0)
	for _, item := range repeated.PersonalMemories {
		if item.Kind == "preference" {
			secondOrder = append(secondOrder, item.ID)
		}
	}
	if strings.Join(firstOrder, ",") != strings.Join(secondOrder, ",") {
		t.Fatalf("unstable order = %v then %v", firstOrder, secondOrder)
	}
	knowledgeResult, err := store.RetrieveContext(ctx, "character-search-a", "明年正式发布续作")
	if err != nil {
		t.Fatal(err)
	}
	if len(knowledgeResult.Knowledge) != 1 || knowledgeResult.Knowledge[0].ID != knowledge.ID || knowledgeResult.Knowledge[0].Layer != "knowledge" || len(knowledgeResult.Knowledge[0].Sources) != 1 {
		t.Fatalf("knowledge result = %#v", knowledgeResult.Knowledge)
	}
}

func TestPostgresTrigramQueriesUseGINIndexes(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := pgstore.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Raw().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	personalPlan := explainPostgresPlan(t, ctx, tx, "EXPLAIN (COSTS OFF) SELECT id FROM personal_memories WHERE content ILIKE '%' || $1 || '%'", "安静陪伴")
	if !strings.Contains(personalPlan, "personal_memories_content_trgm") {
		t.Fatalf("personal plan does not use trigram index:\n%s", personalPlan)
	}
	topicPlan := explainPostgresPlan(t, ctx, tx, "EXPLAIN (COSTS OFF) SELECT id FROM knowledge_entries WHERE topic ILIKE '%' || $1 || '%'", "发布情报")
	if !strings.Contains(topicPlan, "knowledge_entries_topic_trgm") {
		t.Fatalf("topic plan does not use trigram index:\n%s", topicPlan)
	}
	statementPlan := explainPostgresPlan(t, ctx, tx, "EXPLAIN (COSTS OFF) SELECT id FROM knowledge_entries WHERE statement ILIKE '%' || $1 || '%'", "续作更新")
	if !strings.Contains(statementPlan, "knowledge_entries_statement_trgm") {
		t.Fatalf("statement plan does not use trigram index:\n%s", statementPlan)
	}
}

func containsRetrievedPersonalID(records []RetrievedPersonalMemory, id string) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}

func explainPostgresPlan(t *testing.T, ctx context.Context, tx pgx.Tx, query string, arg string) string {
	t.Helper()
	rows, err := tx.Query(ctx, query, arg)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	lines := make([]string, 0)
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

func assertPostgresEmbeddingOutbox(t *testing.T, ctx context.Context, pool *pgstore.Pool, itemKind, itemID, content string) {
	t.Helper()
	wantPointID, err := vectorindex.PointID(itemKind, itemID, SemanticEmbeddingModelID)
	if err != nil {
		t.Fatal(err)
	}
	var itemPointID, jobPointID string
	var itemHash, jobHash, itemStatus, jobStatus string
	if err := pool.Raw().QueryRow(ctx, "SELECT point_id::text, content_hash, status FROM memory_embedding_items WHERE item_kind = $1 AND item_id = $2 AND model_id = $3", itemKind, itemID, SemanticEmbeddingModelID).Scan(&itemPointID, &itemHash, &itemStatus); err != nil {
		t.Fatalf("query embedding item: %v", err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT point_id::text, content_hash, status FROM memory_embedding_jobs WHERE item_kind = $1 AND item_id = $2 AND model_id = $3", itemKind, itemID, SemanticEmbeddingModelID).Scan(&jobPointID, &jobHash, &jobStatus); err != nil {
		t.Fatalf("query embedding job: %v", err)
	}
	wantHash := semanticContentHash(content)
	if itemPointID != wantPointID.String() || jobPointID != wantPointID.String() || itemHash != wantHash || jobHash != wantHash || itemStatus != "pending" || jobStatus != "pending" {
		t.Fatalf("item=(%s,%s,%s) job=(%s,%s,%s), want point=%s hash=%s pending", itemPointID, itemHash, itemStatus, jobPointID, jobHash, jobStatus, wantPointID, wantHash)
	}
}

func openIsolatedPostgresStore(t *testing.T, ctx context.Context) *pgstore.Pool {
	t.Helper()
	databaseURL := os.Getenv("FAIRY_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable"
	}
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("fairy_memory_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cleanup, err := pgxpool.New(cleanupCtx, databaseURL)
		if err != nil {
			t.Logf("open cleanup pool: %v", err)
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})
	pool, err := pgstore.Open(ctx, pgstore.ShortTimeoutConfig(withPostgresSearchPath(t, databaseURL, schema)))
	if err != nil {
		t.Fatalf("open postgres store pool: %v", err)
	}
	return pool
}

func withPostgresSearchPath(t *testing.T, rawURL string, schema string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	values := parsed.Query()
	values.Set("search_path", schema)
	parsed.RawQuery = values.Encode()
	return parsed.String()
}
