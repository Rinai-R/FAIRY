//go:build integration

package memory

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"fairy/coredb"
	dbschema "fairy/coredb/schema"
	vectorindex "fairy/vectorindex"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type staticSemanticIndex struct {
	hits []vectorindex.SearchHit
}

func (s staticSemanticIndex) Ready(context.Context) error { return nil }

func (s staticSemanticIndex) Search(context.Context, []float32, string, string, int) ([]vectorindex.SearchHit, error) {
	return append([]vectorindex.SearchHit(nil), s.hits...), nil
}

func TestPostgresStoreSummaryUsesInjectedPool(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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

func TestPostgresConversationPromptContextBoundsHistoryMaterialization(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	const (
		activeCount  = 4
		contentBytes = 128
	)
	for _, fixture := range []struct {
		name           string
		conversationID string
		compactedCount int
	}{
		{name: "zero_cutoff", conversationID: "conversation-prompt-context-zero", compactedCount: 0},
		{name: "small_history", conversationID: "conversation-prompt-context-small", compactedCount: 8},
		{name: "large_history", conversationID: "conversation-prompt-context-large", compactedCount: 4000},
	} {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			seedPostgresPromptContextFixture(t, ctx, pool, fixture.conversationID, fixture.compactedCount, activeCount, contentBytes)
			store, err := NewStoreFromPool(pool)
			if err != nil {
				t.Fatalf("NewStoreFromPool: %v", err)
			}
			bootstrap, err := store.LoadConversationContext(ctx, fixture.conversationID)
			if err != nil {
				t.Fatalf("LoadConversationContext: %v", err)
			}
			if got, want := len(bootstrap.Messages), fixture.compactedCount+activeCount; got != want {
				t.Fatalf("full loader messages = %d, want %d", got, want)
			}
			promptContext, err := store.LoadConversationPromptContext(ctx, fixture.conversationID)
			if err != nil {
				t.Fatalf("LoadConversationPromptContext: %v", err)
			}
			if promptContext.Conversation != bootstrap.Conversation || !reflect.DeepEqual(promptContext.PromptWindow, bootstrap.PromptWindow) {
				t.Fatalf("prompt context metadata = %#v/%#v, want %#v/%#v", promptContext.Conversation, promptContext.PromptWindow, bootstrap.Conversation, bootstrap.PromptWindow)
			}
			materializedBytes := 0
			for index, message := range promptContext.Messages {
				materializedBytes += len(message.Content)
				wantSequence := uint64(fixture.compactedCount + index + 1)
				if message.Sequence != wantSequence {
					t.Fatalf("active message %d sequence = %d, want %d", index, message.Sequence, wantSequence)
				}
			}
			if got, want := len(promptContext.Messages), activeCount; got != want {
				t.Fatalf("active loader messages = %d, want %d", got, want)
			}
			if got, want := materializedBytes, activeCount*contentBytes; got != want {
				t.Fatalf("active loader content bytes = %d, want %d", got, want)
			}
			t.Logf("full messages=%d active messages=%d active contentBytes=%d", len(bootstrap.Messages), len(promptContext.Messages), materializedBytes)
		})
	}
}

func seedPostgresPromptContextFixture(t testing.TB, ctx context.Context, pool *coredb.Pool, conversationID string, compactedCount, activeCount, contentBytes int) {
	t.Helper()
	totalCount := compactedCount + activeCount
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
VALUES ($1, 'character-prompt-context', 1, $2)`, conversationID, totalCount); err != nil {
		t.Fatalf("seed prompt context conversation: %v", err)
	}
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO prompt_windows(conversation_id, revision, summary, cutoff_message_sequence, updated_at_ms)
VALUES ($1, 2, '已压缩历史', $2, $3)`, conversationID, compactedCount, totalCount); err != nil {
		t.Fatalf("seed prompt window: %v", err)
	}
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms)
SELECT $1 || '-turn-' || sequence, $1, sequence, 'completed', 'user', 'ineligible', sequence, sequence
FROM generate_series(1, $2) AS sequence`, conversationID, totalCount); err != nil {
		t.Fatalf("seed prompt context turns: %v", err)
	}
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms)
SELECT $1 || '-message-' || sequence, $1, $1 || '-turn-' || sequence, sequence,
       CASE WHEN sequence % 2 = 0 THEN 'assistant' ELSE 'user' END,
       repeat('x', $3), sequence
FROM generate_series(1, $2) AS sequence`, conversationID, totalCount, contentBytes); err != nil {
		t.Fatalf("seed prompt context messages: %v", err)
	}
}

var benchmarkConversationPromptContextMessages []MessageRecord

func BenchmarkPostgresConversationPromptContextHistoryGrowth(b *testing.B) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(b, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		b.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		b.Fatalf("NewStoreFromPool: %v", err)
	}
	const (
		activeCount  = 4
		contentBytes = 128
	)
	fixtures := []struct {
		name           string
		conversationID string
		compactedCount int
	}{
		{name: "small", conversationID: "benchmark-prompt-context-small", compactedCount: 8},
		{name: "large", conversationID: "benchmark-prompt-context-large", compactedCount: 4000},
	}
	for _, fixture := range fixtures {
		seedPostgresPromptContextFixture(b, ctx, pool, fixture.conversationID, fixture.compactedCount, activeCount, contentBytes)
	}
	for _, mode := range []string{"full", "active"} {
		for _, fixture := range fixtures {
			mode := mode
			fixture := fixture
			b.Run(mode+"/"+fixture.name, func(b *testing.B) {
				b.ReportAllocs()
				var messages []MessageRecord
				b.ResetTimer()
				for range b.N {
					if mode == "full" {
						loaded, loadErr := store.LoadConversationContext(ctx, fixture.conversationID)
						if loadErr != nil {
							b.Fatal(loadErr)
						}
						messages = loaded.Messages
						continue
					}
					loaded, loadErr := store.LoadConversationPromptContext(ctx, fixture.conversationID)
					if loadErr != nil {
						b.Fatal(loadErr)
					}
					messages = loaded.Messages
				}
				b.StopTimer()
				materializedBytes := 0
				for _, message := range messages {
					materializedBytes += len(message.Content)
				}
				b.ReportMetric(float64(len(messages)), "rows/op")
				b.ReportMetric(float64(materializedBytes), "content-bytes/op")
				benchmarkConversationPromptContextMessages = messages
			})
		}
	}
}

func TestPostgresConversationActivityBoundsExpiredHistory(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	const nowUnixMS = int64(1_800_000_000_000)
	for _, fixture := range []struct {
		name           string
		conversationID string
		expiredCount   int
	}{
		{name: "small_history", conversationID: "conversation-activity-small", expiredCount: 8},
		{name: "large_history", conversationID: "conversation-activity-large", expiredCount: 4000},
	} {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			seedPostgresConversationActivityFixture(t, ctx, pool, fixture.conversationID, fixture.expiredCount, nowUnixMS)
			full, err := store.LoadConversationContext(ctx, fixture.conversationID)
			if err != nil {
				t.Fatalf("LoadConversationContext: %v", err)
			}
			if got, want := len(full.Messages), fixture.expiredCount+7; got != want {
				t.Fatalf("full messages = %d, want %d", got, want)
			}
			activity, err := store.LoadConversationActivityContext(ctx, fixture.conversationID, nowUnixMS)
			if err != nil {
				t.Fatalf("LoadConversationActivityContext: %v", err)
			}
			if activity.Conversation != full.Conversation {
				t.Fatalf("activity conversation = %#v, want %#v", activity.Conversation, full.Conversation)
			}
			if activity.AssistantMessages5Minutes != 2 || activity.AssistantMessages30Minutes != 4 || activity.UserMessages30Minutes != 2 {
				t.Fatalf("activity counts = %#v", activity)
			}
			wantLatest := nowUnixMS - time.Minute.Milliseconds()
			if activity.LastAssistantMessageAtUnixMS == nil || *activity.LastAssistantMessageAtUnixMS != wantLatest {
				t.Fatalf("latest assistant = %#v, want %d", activity.LastAssistantMessageAtUnixMS, wantLatest)
			}
			record, err := store.LoadConversationRecordContext(ctx, fixture.conversationID)
			if err != nil || record != full.Conversation {
				t.Fatalf("LoadConversationRecordContext = %#v, %v", record, err)
			}
			t.Logf("full messages=%d activity rows=1 recent assistant=%d/%d recent user=%d", len(full.Messages), activity.AssistantMessages5Minutes, activity.AssistantMessages30Minutes, activity.UserMessages30Minutes)
		})
	}

	if _, err := store.LoadConversationActivityContext(ctx, "conversation-activity-large", 0); err == nil {
		t.Fatal("zero activity evaluation time accepted")
	}
	if _, err := pool.Raw().Exec(ctx, `
UPDATE conversation_messages
SET created_at_ms = $2
WHERE conversation_id = $1 AND sequence = 4007`, "conversation-activity-large", nowUnixMS+1); err != nil {
		t.Fatalf("seed future assistant timestamp: %v", err)
	}
	if _, err := store.LoadConversationActivityContext(ctx, "conversation-activity-large", nowUnixMS); err == nil {
		t.Fatal("future assistant timestamp accepted")
	}

	tx, err := pool.Raw().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	plan := explainPostgresPlan(t, ctx, tx, "EXPLAIN (COSTS OFF) "+conversationActivityQuery,
		"conversation-activity-small",
		nowUnixMS-30*time.Minute.Milliseconds(),
		nowUnixMS-5*time.Minute.Milliseconds(),
	)
	if !strings.Contains(plan, "conversation_messages_conversation_role_created") {
		t.Fatalf("activity plan does not use role/time index:\n%s", plan)
	}
}

func TestPostgresConversationRecordDoesNotRequireTranscriptTables(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Raw().Exec(ctx, "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ('metadata-only', 'character-metadata', 1, 2)"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Raw().Exec(ctx, "DROP TABLE prompt_windows, conversation_messages CASCADE"); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.LoadConversationRecordContext(ctx, "metadata-only")
	if err != nil {
		t.Fatalf("LoadConversationRecordContext: %v", err)
	}
	if record.ID != "metadata-only" || record.CharacterID != "character-metadata" || record.CreatedAtUnixMS != 1 || record.UpdatedAtUnixMS != 2 {
		t.Fatalf("record = %#v", record)
	}
}

func TestPostgresConversationActivityKeepsOldLatestAndEmptyHistory(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	const nowUnixMS = int64(1_800_000_000_000)
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
VALUES ('activity-empty', 'character-activity', 1, $1),
	       ('activity-old-latest', 'character-activity', 1, $1)`, nowUnixMS); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms)
	VALUES ('activity-old-latest-turn', 'activity-old-latest', 1, 'completed', 'user', 'ineligible', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms)
	VALUES ('activity-old-latest-message', 'activity-old-latest', 'activity-old-latest-turn', 1, 'assistant', 'old', $1)`, nowUnixMS-40*time.Minute.Milliseconds()); err != nil {
		t.Fatal(err)
	}
	empty, err := store.LoadConversationActivityContext(ctx, "activity-empty", nowUnixMS)
	if err != nil {
		t.Fatal(err)
	}
	if empty.AssistantMessages5Minutes != 0 || empty.AssistantMessages30Minutes != 0 || empty.UserMessages30Minutes != 0 || empty.LastAssistantMessageAtUnixMS != nil {
		t.Fatalf("empty activity = %#v", empty)
	}
	oldLatest, err := store.LoadConversationActivityContext(ctx, "activity-old-latest", nowUnixMS)
	if err != nil {
		t.Fatal(err)
	}
	wantLatest := nowUnixMS - 40*time.Minute.Milliseconds()
	if oldLatest.AssistantMessages5Minutes != 0 || oldLatest.AssistantMessages30Minutes != 0 || oldLatest.UserMessages30Minutes != 0 || oldLatest.LastAssistantMessageAtUnixMS == nil || *oldLatest.LastAssistantMessageAtUnixMS != wantLatest {
		t.Fatalf("old latest activity = %#v, want latest %d", oldLatest, wantLatest)
	}
}

func seedPostgresConversationActivityFixture(t testing.TB, ctx context.Context, pool *coredb.Pool, conversationID string, expiredCount int, nowUnixMS int64) {
	t.Helper()
	const recentCount = 7
	totalCount := expiredCount + recentCount
	if _, err := pool.Raw().Exec(ctx, "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ($1, 'character-activity', 1, $2)", conversationID, nowUnixMS); err != nil {
		t.Fatalf("seed activity conversation: %v", err)
	}
	if _, err := pool.Raw().Exec(ctx, "INSERT INTO prompt_windows(conversation_id, revision, summary, cutoff_message_sequence, updated_at_ms) VALUES ($1, 1, NULL, 0, $2)", conversationID, nowUnixMS); err != nil {
		t.Fatalf("seed activity prompt window: %v", err)
	}
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO conversation_turns(id, conversation_id, sequence, status, origin, extraction_state, created_at_ms, updated_at_ms)
SELECT $1 || '-turn-' || sequence, $1, sequence, 'completed', 'user', 'ineligible', sequence, sequence
FROM generate_series(1, $2) AS sequence`, conversationID, totalCount); err != nil {
		t.Fatalf("seed activity turns: %v", err)
	}
	if expiredCount > 0 {
		if _, err := pool.Raw().Exec(ctx, `
INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms)
SELECT $1 || '-message-' || sequence, $1, $1 || '-turn-' || sequence, sequence,
       CASE WHEN sequence % 2 = 0 THEN 'assistant' ELSE 'user' END,
       repeat('x', 128), $3::bigint - $4::bigint - sequence
FROM generate_series(1, $2) AS sequence`, conversationID, expiredCount, nowUnixMS, 31*time.Minute.Milliseconds()); err != nil {
			t.Fatalf("seed expired activity messages: %v", err)
		}
	}
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms)
SELECT $1 || '-message-' || ($2 + recent.sequence_offset), $1, $1 || '-turn-' || ($2 + recent.sequence_offset), $2 + recent.sequence_offset,
       recent.role, repeat('r', 128), recent.created_at_ms
FROM (VALUES
    (1, 'assistant', $3::bigint - $4::bigint - 1),
    (2, 'user',      $3::bigint - $4::bigint),
    (3, 'assistant', $3::bigint - $4::bigint),
    (4, 'assistant', $3::bigint - $5::bigint - 1),
    (5, 'assistant', $3::bigint - $5::bigint),
    (6, 'user',      $3::bigint - $6::bigint),
    (7, 'assistant', $3::bigint - $7::bigint)
) AS recent(sequence_offset, role, created_at_ms)`,
		conversationID,
		expiredCount,
		nowUnixMS,
		30*time.Minute.Milliseconds(),
		5*time.Minute.Milliseconds(),
		10*time.Minute.Milliseconds(),
		time.Minute.Milliseconds(),
	); err != nil {
		t.Fatalf("seed recent activity messages: %v", err)
	}
}

var (
	benchmarkConversationActivity       ConversationActivity
	benchmarkConversationActivityRecord ConversationRecord
)

func BenchmarkPostgresConversationActivityHistoryGrowth(b *testing.B) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(b, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		b.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		b.Fatal(err)
	}
	const nowUnixMS = int64(1_800_000_000_000)
	fixtures := []struct {
		name           string
		conversationID string
		expiredCount   int
	}{
		{name: "small", conversationID: "benchmark-activity-small", expiredCount: 8},
		{name: "large", conversationID: "benchmark-activity-large", expiredCount: 4000},
	}
	for _, fixture := range fixtures {
		seedPostgresConversationActivityFixture(b, ctx, pool, fixture.conversationID, fixture.expiredCount, nowUnixMS)
	}
	for _, mode := range []string{"full", "activity", "metadata"} {
		for _, fixture := range fixtures {
			mode := mode
			fixture := fixture
			b.Run(mode+"/"+fixture.name, func(b *testing.B) {
				b.ReportAllocs()
				var messages []MessageRecord
				var activity ConversationActivity
				var record ConversationRecord
				b.ResetTimer()
				for range b.N {
					switch mode {
					case "full":
						loaded, loadErr := store.LoadConversationContext(ctx, fixture.conversationID)
						if loadErr != nil {
							b.Fatal(loadErr)
						}
						messages = loaded.Messages
					case "activity":
						activity, err = store.LoadConversationActivityContext(ctx, fixture.conversationID, nowUnixMS)
						if err != nil {
							b.Fatal(err)
						}
					case "metadata":
						record, err = store.LoadConversationRecordContext(ctx, fixture.conversationID)
						if err != nil {
							b.Fatal(err)
						}
					}
				}
				b.StopTimer()
				materializedBytes := 0
				for _, message := range messages {
					materializedBytes += len(message.Content)
				}
				rows := len(messages)
				if mode != "full" {
					rows = 1
				}
				b.ReportMetric(float64(rows), "rows/op")
				b.ReportMetric(float64(materializedBytes), "content-bytes/op")
				benchmarkConversationPromptContextMessages = messages
				benchmarkConversationActivity = activity
				benchmarkConversationActivityRecord = record
			})
		}
	}
}

func TestPostgresDesktopInitiationTurnDoesNotFabricateUserMessage(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-desktop-initiation")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginInitiationTurnContext(ctx, bootstrap.Conversation.ID, []string{"obs-1"})
	if err != nil {
		t.Fatal(err)
	}
	var origin, extractionState string
	var messageCount int
	if err := pool.Raw().QueryRow(ctx, "SELECT origin, extraction_state FROM conversation_turns WHERE id = $1", turn.ID).Scan(&origin, &extractionState); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM conversation_messages WHERE turn_id = $1", turn.ID).Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if origin != "desktop_initiation" || extractionState != "ineligible" || messageCount != 0 {
		t.Fatalf("origin=%q extraction=%q messages=%d", origin, extractionState, messageCount)
	}
	var evidenceID string
	if err := pool.Raw().QueryRow(ctx, "SELECT evidence_id FROM conversation_turn_evidence WHERE turn_id = $1", turn.ID).Scan(&evidenceID); err != nil {
		t.Fatal(err)
	}
	if evidenceID != "obs-1" {
		t.Fatalf("evidence_id = %q", evidenceID)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "欢迎回来。"); err != nil {
		t.Fatal(err)
	}
	var status, role, content string
	if err := pool.Raw().QueryRow(ctx, "SELECT status, extraction_state FROM conversation_turns WHERE id = $1", turn.ID).Scan(&status, &extractionState); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT role, content FROM conversation_messages WHERE turn_id = $1", turn.ID).Scan(&role, &content); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || extractionState != "ineligible" || role != "assistant" || content != "欢迎回来。" {
		t.Fatalf("status=%q extraction=%q role=%q content=%q", status, extractionState, role, content)
	}
}

func TestPostgresConversationFailedTurnPreservesUserOnly(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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

func TestPostgresPersonalMemoryContentLimitPreservesWritesAndRejectsOversizedHistory(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-content-limit")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "memory content source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "memory content reply"); err != nil {
		t.Fatal(err)
	}

	exact := strings.Repeat("界", MaxPersonalMemoryContentRunes)
	created, err := store.CreatePersonalMemoryContext(ctx, "preference", MemoryScope{Type: "global"}, exact, 9000)
	if err != nil {
		t.Fatalf("creating exact-limit memory: %v", err)
	}
	assertPostgresEmbeddingOutbox(t, ctx, pool, vectorindex.ItemKindPersonalMemory, created.ID, exact)
	reviseSource, err := store.CreatePersonalMemoryContext(ctx, "profile", MemoryScope{Type: "global"}, "revise source", 8000)
	if err != nil {
		t.Fatalf("creating exact-limit revise source: %v", err)
	}
	revisedExact, err := store.RevisePersonalMemoryContext(ctx, reviseSource.ID, exact, 9100)
	if err != nil {
		t.Fatalf("revising to exact-limit memory: %v", err)
	}
	if revisedExact.SupersedesID == nil || *revisedExact.SupersedesID != reviseSource.ID {
		t.Fatalf("exact-limit revision = %#v", revisedExact)
	}
	assertPostgresEmbeddingOutbox(t, ctx, pool, vectorindex.ItemKindPersonalMemory, revisedExact.ID, exact)
	tooLong := exact + "界"
	if _, err := store.CreatePersonalMemoryContext(ctx, "preference", MemoryScope{Type: "global"}, tooLong, 9000); err == nil || !strings.Contains(err.Error(), "2400") {
		t.Fatalf("oversized create error = %v", err)
	}
	if _, err := store.RevisePersonalMemoryContext(ctx, created.ID, tooLong, 9000); err == nil || !strings.Contains(err.Error(), "2400") {
		t.Fatalf("oversized revise error = %v", err)
	}
	var createdStatus string
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM personal_memories WHERE id = $1", created.ID).Scan(&createdStatus); err != nil || createdStatus != "active" {
		t.Fatalf("created status after rejected revise = %q, %v", createdStatus, err)
	}

	_, exactTurnID, exactBatchID := seedPostgresRunningExtractionBatch(t, ctx, pool, store, "character-content-limit")
	exactResult, err := store.CommitMemoryMutationsContext(ctx, exactBatchID, "character-content-limit", nil, []MemoryMutation{{
		Operation: "create", SourceTurnID: exactTurnID, Kind: "experience", Scope: MemoryScope{Type: "global"}, Content: exact, ConfidenceBasisPoints: 8500,
	}})
	if err != nil || len(exactResult) != 1 || exactResult[0].Status != "applied" {
		t.Fatalf("exact-limit extraction result = %#v, %v", exactResult, err)
	}
	assertPostgresEmbeddingOutbox(t, ctx, pool, vectorindex.ItemKindPersonalMemory, exactResult[0].MemoryID, exact)

	_, rollbackTurnID, rollbackBatchID := seedPostgresRunningExtractionBatch(t, ctx, pool, store, "character-content-limit")
	_, err = store.CommitMemoryMutationsContext(ctx, rollbackBatchID, "character-content-limit", nil, []MemoryMutation{
		{Operation: "create", SourceTurnID: rollbackTurnID, Kind: "profile", Scope: MemoryScope{Type: "global"}, Content: "valid-before-oversized", ConfidenceBasisPoints: 8000},
		{Operation: "create", SourceTurnID: rollbackTurnID, Kind: "experience", Scope: MemoryScope{Type: "global"}, Content: tooLong, ConfidenceBasisPoints: 8000},
	})
	if err == nil || !strings.Contains(err.Error(), "2400") {
		t.Fatalf("oversized extraction error = %v", err)
	}
	var leakedMemories, leakedItems, leakedJobs int
	var rollbackBatchStatus, rollbackTurnState string
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM personal_memories WHERE content = 'valid-before-oversized'").Scan(&leakedMemories); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM memory_embedding_items WHERE item_id IN (SELECT id FROM personal_memories WHERE content = 'valid-before-oversized')").Scan(&leakedItems); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM memory_embedding_jobs WHERE item_id IN (SELECT id FROM personal_memories WHERE content = 'valid-before-oversized')").Scan(&leakedJobs); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM extraction_batches WHERE id = $1", rollbackBatchID).Scan(&rollbackBatchStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT extraction_state FROM conversation_turns WHERE id = $1", rollbackTurnID).Scan(&rollbackTurnState); err != nil {
		t.Fatal(err)
	}
	if leakedMemories != 0 || leakedItems != 0 || leakedJobs != 0 || rollbackBatchStatus != "running" || rollbackTurnState != "claimed" {
		t.Fatalf("oversized extraction leaked memory=%d item=%d job=%d batch=%q turn=%q", leakedMemories, leakedItems, leakedJobs, rollbackBatchStatus, rollbackTurnState)
	}

	legacyID := "legacy-oversized-content"
	insertOversizedPersonalMemory(t, ctx, pool, legacyID, "relationship", "unassigned_legacy", nil, "needs_review", tooLong, bootstrap.Conversation.ID, turn.ID)
	if _, err := store.AssignLegacyRelationshipContext(ctx, legacyID, "character-content-limit"); err == nil || !strings.Contains(err.Error(), "2400") {
		t.Fatalf("oversized legacy assignment error = %v", err)
	}
	var legacyStatus, legacyReview string
	if err := pool.Raw().QueryRow(ctx, "SELECT status, review_status FROM personal_memories WHERE id = $1", legacyID).Scan(&legacyStatus, &legacyReview); err != nil {
		t.Fatal(err)
	}
	if legacyStatus != "active" || legacyReview != "needs_review" {
		t.Fatalf("legacy after rejected assignment = (%q, %q)", legacyStatus, legacyReview)
	}

	historyID := "history-oversized-content"
	insertOversizedPersonalMemory(t, ctx, pool, historyID, "profile", "global", nil, "ready", "历史超限标记"+tooLong, bootstrap.Conversation.ID, turn.ID)
	catalog, err := store.PersonalMemoryCatalogContext(ctx, "character-content-limit")
	if err != nil {
		t.Fatal(err)
	}
	if !containsPersonalMemoryRecordID(catalog.Global, historyID) || !containsPersonalMemoryRecordID(catalog.NeedsReview, legacyID) {
		t.Fatalf("catalog omitted oversized repair records: %#v", catalog)
	}
	if _, err := store.RetrieveContext(ctx, "character-content-limit", "历史超限标记"); err == nil || !strings.Contains(err.Error(), historyID) || !strings.Contains(err.Error(), "2400") {
		t.Fatalf("text retrieval oversized history error = %v", err)
	}
	if _, err := store.CompanionPortraitContext(ctx, "character-content-limit"); err == nil || !strings.Contains(err.Error(), historyID) || !strings.Contains(err.Error(), "2400") {
		t.Fatalf("portrait oversized history error = %v", err)
	}
	pointID, err := vectorindex.PointID(vectorindex.ItemKindPersonalMemory, historyID, SemanticEmbeddingModelID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO memory_embedding_items(id, item_kind, item_id, model_id, dimensions, point_id, content_hash, status, embedded_at_ms, created_at_ms, updated_at_ms)
VALUES ($1, 'personal_memory', $2, $3, $4, $5, $6, 'embedded', 1, 1, 1)`, uuid.NewString(), historyID, SemanticEmbeddingModelID, SemanticEmbeddingDimensions, pointID, semanticContentHash("历史超限标记"+tooLong)); err != nil {
		t.Fatal(err)
	}
	vector := make([]float32, SemanticEmbeddingDimensions)
	vector[0] = 1
	_, err = store.RetrieveWithSemanticVectorIndex(ctx, "character-content-limit", "历史超限标记", postgresWorkerEmbedder{vector: vector}, staticSemanticIndex{hits: []vectorindex.SearchHit{{
		PointID: pointID, ItemKind: vectorindex.ItemKindPersonalMemory, ItemID: historyID,
		ModelID: SemanticEmbeddingModelID, ScopeType: "global", ContentHash: semanticContentHash("历史超限标记" + tooLong), Score: 1,
	}}})
	if err == nil || !strings.Contains(err.Error(), historyID) || !strings.Contains(err.Error(), "2400") {
		t.Fatalf("semantic truth oversized history error = %v", err)
	}

	revised, err := store.RevisePersonalMemoryContext(ctx, historyID, "历史记录已修复", 9200)
	if err != nil {
		t.Fatalf("repairing oversized history: %v", err)
	}
	if revised.SupersedesID == nil || *revised.SupersedesID != historyID {
		t.Fatalf("repaired history = %#v", revised)
	}
	if err := store.TombstonePersonalMemoryContext(ctx, legacyID); err != nil {
		t.Fatalf("tombstoning oversized legacy history: %v", err)
	}
}

func insertOversizedPersonalMemory(t *testing.T, ctx context.Context, pool *coredb.Pool, id, kind, scopeKind string, characterID *string, reviewStatus, content, conversationID, turnID string) {
	t.Helper()
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO personal_memories(
  id, kind, scope_kind, character_id, review_status, content, status,
  confidence_basis_points, source_conversation_id, source_turn_id, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, 'active', 10000, $7, $8, 1, 1)`, id, kind, scopeKind, characterID, reviewStatus, content, conversationID, turnID); err != nil {
		t.Fatal(err)
	}
}

func containsPersonalMemoryRecordID(records []PersonalMemoryRecord, id string) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}

func TestPostgresKnowledgeLifecyclePreservesSourcesAndOutbox(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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

func TestPostgresCommitCompactionAtomicallySwitchesWindowAndContinuation(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-atomic-compaction")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, "用户消息")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, "助手消息"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveLaneContinuationContext(ctx, LaneContinuationRecord{
		ConversationID:     bootstrap.Conversation.ID,
		Lane:               PromptLaneRespond,
		PreviousResponseID: "resp-atomic",
		RequestShapeHash:   strings.Repeat("a", 64),
		InputPrefixHash:    strings.Repeat("b", 64),
		ResponseItemHash:   strings.Repeat("c", 64),
		WindowRevision:     1,
	}); err != nil {
		t.Fatal(err)
	}
	windowID := "window-atomic"
	result, err := store.CommitCompactionContext(ctx, bootstrap.Conversation.ID, 1, "摘要", ContextWindowRecord{
		ConversationID:       bootstrap.Conversation.ID,
		Lane:                 PromptLaneRespond,
		WindowNumber:         1,
		FirstWindowID:        windowID,
		WindowID:             windowID,
		LastTrigger:          "compaction_committed",
		PromptWindowRevision: 2,
	}, PromptLaneRespond)
	if err != nil || result.WindowRevision != 2 {
		t.Fatalf("CommitCompactionContext() = %#v, %v", result, err)
	}
	updated, err := store.LoadConversationContext(ctx, bootstrap.Conversation.ID)
	if err != nil || updated.PromptWindow.Revision != 2 || updated.PromptWindow.Summary == nil || *updated.PromptWindow.Summary != "摘要" {
		t.Fatalf("prompt window after atomic commit = %#v, %v", updated.PromptWindow, err)
	}
	if _, ok, err := store.LoadLaneContinuationContext(ctx, bootstrap.Conversation.ID, PromptLaneRespond); err != nil || ok {
		t.Fatalf("continuation after atomic commit = (%v, %v)", ok, err)
	}
	contextWindow, ok, err := store.LoadContextWindowContext(ctx, bootstrap.Conversation.ID, PromptLaneRespond)
	if err != nil || !ok || contextWindow.PromptWindowRevision != 2 {
		t.Fatalf("context window after atomic commit = %#v, (%v, %v)", contextWindow, ok, err)
	}
}

func TestPostgresCommitMemoryMutationsCommitsRowsAndOutboxAtomically(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID, batchID := seedPostgresRunningExtractionBatch(t, ctx, pool, store, "character-mutation-success")
	results, err := store.CommitMemoryMutationsContext(ctx, batchID, "character-mutation-success", nil, []MemoryMutation{
		{Operation: "create", SourceTurnID: turnID, Kind: "preference", Scope: MemoryScope{Type: "global"}, Content: "喜欢爵士乐", ConfidenceBasisPoints: 9000},
		{Operation: "create", SourceTurnID: turnID, Kind: "relationship", Scope: MemoryScope{Type: "character", CharacterID: "character-mutation-success"}, Content: "愿意分享近况", ConfidenceBasisPoints: 8500},
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

func TestPostgresCommitMemoryMutationsPreservesPerMutationSourceTurn(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	_, turnIDs, batchID := seedPostgresRunningExtractionBatchWithTurns(t, ctx, pool, store, "character-mutation-evidence", 2)
	results, err := store.CommitMemoryMutationsContext(ctx, batchID, "character-mutation-evidence", nil, []MemoryMutation{
		{Operation: "create", SourceTurnID: turnIDs[0], Kind: "preference", Scope: MemoryScope{Type: "global"}, Content: "第一条证据喜欢爵士乐", ConfidenceBasisPoints: 9000},
		{Operation: "create", SourceTurnID: turnIDs[1], Kind: "experience", Scope: MemoryScope{Type: "global"}, Content: "第二条证据准备搬家", ConfidenceBasisPoints: 8500},
	})
	if err != nil {
		t.Fatalf("CommitMemoryMutationsContext: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %#v", results)
	}
	for index, result := range results {
		var sourceTurnID string
		if err := pool.Raw().QueryRow(ctx, "SELECT source_turn_id FROM personal_memories WHERE id = $1", result.MemoryID).Scan(&sourceTurnID); err != nil {
			t.Fatal(err)
		}
		if sourceTurnID != turnIDs[index] {
			t.Fatalf("memory %d source turn = %q, want %q", index, sourceTurnID, turnIDs[index])
		}
	}
}

func TestPostgresCommitMemoryMutationsCopiesObservationEvidenceProvenance(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	_, turnID, batchID := seedPostgresRunningExtractionBatch(t, ctx, pool, store, "character-memory-observation-provenance")
	if _, err := pool.Raw().Exec(ctx, `INSERT INTO conversation_turn_evidence(turn_id, evidence_id, created_at_ms) VALUES ($1, $2, $3), ($1, $4, $3)`, turnID, "observation-a", 1, "observation-b"); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
	results, err := store.CommitMemoryMutationsContext(ctx, batchID, "character-memory-observation-provenance", nil, []MemoryMutation{{
		Operation: "create", SourceTurnID: turnID, Kind: "preference", Scope: MemoryScope{Type: "global"}, Content: "观察到的长期偏好", ConfidenceBasisPoints: 9000,
	}})
	if err != nil {
		t.Fatalf("commit mutation: %v", err)
	}
	if len(results) != 1 || results[0].MemoryID == "" {
		t.Fatalf("results = %#v", results)
	}
	rows, err := pool.Raw().Query(ctx, "SELECT turn_id, evidence_id FROM personal_memory_evidence WHERE memory_id = $1 ORDER BY evidence_id", results[0].MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var sourceTurn, evidenceID string
		if err := rows.Scan(&sourceTurn, &evidenceID); err != nil {
			t.Fatal(err)
		}
		if sourceTurn != turnID {
			t.Fatalf("provenance turn = %q, want %q", sourceTurn, turnID)
		}
		got = append(got, evidenceID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"observation-a", "observation-b"}) {
		t.Fatalf("provenance evidence = %#v", got)
	}
}

func TestPostgresCommitMemoryMutationsRollsBackEarlierMutationOnLaterFailure(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	_, turnID, batchID := seedPostgresRunningExtractionBatch(t, ctx, pool, store, "character-mutation-rollback")
	_, err = store.CommitMemoryMutationsContext(ctx, batchID, "character-mutation-rollback", nil, []MemoryMutation{
		{Operation: "create", SourceTurnID: turnID, Kind: "preference", Scope: MemoryScope{Type: "global"}, Content: "must rollback", ConfidenceBasisPoints: 9000},
		{Operation: "supersede", SourceTurnID: turnID, MemoryID: "not-allowed-memory", Kind: "profile", Scope: MemoryScope{Type: "global"}, Content: "later failure", ConfidenceBasisPoints: 8000},
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	_, initialTurnID, initialBatchID := seedPostgresRunningExtractionBatch(t, ctx, pool, store, "character-mutation-parity")
	initialResults, err := store.CommitMemoryMutationsContext(ctx, initialBatchID, "character-mutation-parity", nil, []MemoryMutation{{Operation: "create", SourceTurnID: initialTurnID, Kind: "preference", Scope: MemoryScope{Type: "global"}, Content: "喜欢安静", ConfidenceBasisPoints: 9000}})
	if err != nil {
		t.Fatal(err)
	}
	if len(initialResults) != 1 || initialResults[0].Status != "applied" {
		t.Fatalf("initial results = %#v", initialResults)
	}
	initialID := initialResults[0].MemoryID
	_, turnID, batchID := seedPostgresRunningExtractionBatch(t, ctx, pool, store, "character-mutation-parity")
	results, err := store.CommitMemoryMutationsContext(ctx, batchID, "character-mutation-parity", []string{initialID}, []MemoryMutation{
		{Operation: "create", SourceTurnID: turnID, Kind: "preference", Scope: MemoryScope{Type: "global"}, Content: "喜欢安静", ConfidenceBasisPoints: 9000},
		{Operation: "supersede", SourceTurnID: turnID, MemoryID: initialID, Kind: "preference", Scope: MemoryScope{Type: "global"}, Content: "喜欢清晨散步", ConfidenceBasisPoints: 9300},
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
	var newSourceTurnID string
	if err := pool.Raw().QueryRow(ctx, "SELECT status, supersedes_id, source_turn_id FROM personal_memories WHERE id = $1", results[1].MemoryID).Scan(&newStatus, &supersedesID, &newSourceTurnID); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "superseded" || newStatus != "active" || supersedesID != initialID || newSourceTurnID != turnID {
		t.Fatalf("old=%q new=%q supersedes=%q source=%q", oldStatus, newStatus, supersedesID, newSourceTurnID)
	}
	assertPostgresEmbeddingOutbox(t, ctx, pool, vectorindex.ItemKindPersonalMemory, results[1].MemoryID, "喜欢清晨散步")
}

func TestPostgresCommitMemoryMutationsRejectsBatchExternalTurnAndKeepsEmptyCompletion(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	_, externalTurnID, _ := seedPostgresRunningExtractionBatch(t, ctx, pool, store, "character-mutation-external-source")
	_, allowedTurnID, batchID := seedPostgresRunningExtractionBatch(t, ctx, pool, store, "character-mutation-external")
	_, err = store.CommitMemoryMutationsContext(ctx, batchID, "character-mutation-external", nil, []MemoryMutation{{
		Operation: "create", SourceTurnID: externalTurnID, Kind: "preference", Scope: MemoryScope{Type: "global"}, Content: "外部证据", ConfidenceBasisPoints: 9000,
	}})
	if err == nil || !strings.Contains(err.Error(), "source turn is not provided") {
		t.Fatalf("external evidence error = %v", err)
	}
	var status, extractionState string
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM extraction_batches WHERE id = $1", batchID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT extraction_state FROM conversation_turns WHERE id = $1", allowedTurnID).Scan(&extractionState); err != nil {
		t.Fatal(err)
	}
	if status != "running" || extractionState != "claimed" {
		t.Fatalf("external evidence changed batch state: status=%q turn=%q", status, extractionState)
	}
	if _, err := store.CommitMemoryMutationsContext(ctx, batchID, "character-mutation-external", nil, nil); err != nil {
		t.Fatalf("empty mutation completion: %v", err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM extraction_batches WHERE id = $1", batchID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" {
		t.Fatalf("empty mutation batch status = %q", status)
	}
}

func seedPostgresRunningExtractionBatch(t *testing.T, ctx context.Context, _ *coredb.Pool, store *Store, characterID string) (string, string, string) {
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
	batch, err := store.ClaimExtractionBatchContext(ctx, bootstrap.Conversation.ID, 1)
	if err != nil || batch == nil {
		t.Fatalf("claiming seeded extraction batch = %#v, %v", batch, err)
	}
	if len(batch.Turns) != 1 {
		t.Fatalf("seeded extraction batch turns = %#v, want one", batch.Turns)
	}
	return bootstrap.Conversation.ID, batch.Turns[0].TurnID, batch.BatchID
}

func seedPostgresRunningExtractionBatchWithTurns(t *testing.T, ctx context.Context, _ *coredb.Pool, store *Store, characterID string, count int) (string, []string, string) {
	t.Helper()
	if count < 1 {
		t.Fatal("extraction batch must contain at least one turn")
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, characterID)
	if err != nil {
		t.Fatal(err)
	}
	turnIDs := make([]string, 0, count)
	for index := 1; index <= count; index++ {
		turn, err := store.BeginTurnContext(ctx, bootstrap.Conversation.ID, fmt.Sprintf("batch source %d", index))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, fmt.Sprintf("batch reply %d", index)); err != nil {
			t.Fatal(err)
		}
		turnIDs = append(turnIDs, turn.ID)
	}
	batch, err := store.ClaimExtractionBatchContext(ctx, bootstrap.Conversation.ID, count)
	if err != nil || batch == nil {
		t.Fatalf("claiming seeded extraction batch = %#v, %v", batch, err)
	}
	if len(batch.Turns) != count {
		t.Fatalf("seeded extraction batch turns = %#v, want %d", batch.Turns, count)
	}
	turnIDs = turnIDs[:0]
	for _, batchTurn := range batch.Turns {
		turnIDs = append(turnIDs, batchTurn.TurnID)
	}
	return bootstrap.Conversation.ID, turnIDs, batch.BatchID
}

func TestPostgresExtractionLeasePreventsDuplicateClaimAndRecoversExpiredOwner(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	first, err := NewStoreFromPoolWithLease(pool, "worker-first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStoreFromPoolWithLease(pool, "worker-second", time.Minute)
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
	otherID := "worker-second"
	if owner == "worker-second" {
		ownerStore, otherStore = second, first
		otherID = "worker-first"
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if err := otherStore.CompleteExtractionBatchContext(ctx, claimed.BatchID); err == nil {
		t.Fatal("wrong owner completion error = nil")
	}
	if _, err := pool.Raw().Exec(ctx, "UPDATE extraction_batches SET lease_expires_at_ms = $2 WHERE id = $1", claimed.BatchID, time.Now().UnixMilli()-1); err != nil {
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
	if owner != otherID || attempts != 2 {
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

func TestPostgresExtractionProjectionBoundsLookupAndPreservesEvidence(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	first, err := NewStoreFromPoolWithLease(pool, "worker-projection-first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStoreFromPoolWithLease(pool, "worker-projection-second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := first.OpenOrCreateCharacterConversationContext(ctx, "character-projection")
	if err != nil {
		t.Fatal(err)
	}

	type sourceTurn struct {
		user      string
		assistant string
	}
	sources := []sourceTurn{
		{user: strings.Repeat("前序长文本", 550) + "\n喜欢晚间散步", assistant: "我会记住这件事\t一起慢慢聊"},
		{user: "第二轮\r\n后序检索标记", assistant: "收到了后序检索标记"},
		{user: "第三轮继续补充", assistant: strings.Repeat("回复内容", 250)},
	}
	for _, source := range sources {
		turn, err := first.BeginTurnContext(ctx, bootstrap.Conversation.ID, source.user)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := first.CompleteTurnContext(ctx, bootstrap.Conversation.ID, turn.ID, source.assistant); err != nil {
			t.Fatal(err)
		}
	}
	global, err := first.CreatePersonalMemoryContext(ctx, "preference", MemoryScope{Type: "global"}, "后序检索标记", 9000)
	if err != nil {
		t.Fatal(err)
	}
	current, err := first.CreatePersonalMemoryContext(ctx, "relationship", MemoryScope{Type: "character", CharacterID: "character-projection"}, "后序检索标记", 9000)
	if err != nil {
		t.Fatal(err)
	}
	otherConversation, err := first.OpenOrCreateCharacterConversationContext(ctx, "character-projection-other")
	if err != nil {
		t.Fatal(err)
	}
	otherTurn, err := first.BeginTurnContext(ctx, otherConversation.Conversation.ID, "other source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.CompleteTurnContext(ctx, otherConversation.Conversation.ID, otherTurn.ID, "other reply"); err != nil {
		t.Fatal(err)
	}
	other, err := first.CreatePersonalMemoryContext(ctx, "relationship", MemoryScope{Type: "character", CharacterID: "character-projection-other"}, "后序检索标记", 9000)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := first.ClaimExtractionBatchContext(ctx, bootstrap.Conversation.ID, DefaultExtractionBatchLimit)
	if err != nil || claimed == nil {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if len(claimed.Turns) != len(sources) {
		t.Fatalf("claimed turns = %d, want %d", len(claimed.Turns), len(sources))
	}
	for index, source := range sources {
		if claimed.Turns[index].UserMessage != source.user || claimed.Turns[index].AssistantMessage != source.assistant {
			t.Fatalf("claimed turn %d does not preserve complete evidence: %#v", index, claimed.Turns[index])
		}
	}
	seen := make(map[string]bool, len(claimed.ExistingMemories))
	for _, item := range claimed.ExistingMemories {
		seen[item.ID] = true
	}
	if !seen[global.ID] {
		t.Fatalf("global memory %q was not retrieved from bounded projection: %#v", global.ID, claimed.ExistingMemories)
	}
	if !seen[current.ID] {
		t.Fatalf("current-character relationship memory %q was not retrieved from bounded projection: %#v", current.ID, claimed.ExistingMemories)
	}
	if seen[other.ID] {
		t.Fatalf("other-character relationship memory %q leaked into extraction lookup", other.ID)
	}

	if _, err := pool.Raw().Exec(ctx, "UPDATE extraction_batches SET lease_expires_at_ms = $2 WHERE id = $1", claimed.BatchID, time.Now().UnixMilli()-1); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := second.ClaimExtractionBatchContext(ctx, bootstrap.Conversation.ID, DefaultExtractionBatchLimit)
	if err != nil || reclaimed == nil {
		t.Fatalf("reclaim = %#v, %v", reclaimed, err)
	}
	if reclaimed.BatchID != claimed.BatchID || !reflect.DeepEqual(reclaimed.Turns, claimed.Turns) || !reflect.DeepEqual(reclaimed.ExistingMemories, claimed.ExistingMemories) {
		t.Fatalf("reclaimed batch differs from initial claim:\ninitial: %#v\nreclaimed: %#v", claimed, reclaimed)
	}
}

func TestPostgresExtractionFailedCatalogAndRetryReleaseTurns(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "worker-retry", time.Minute)
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	first, err := NewStoreFromPoolWithLease(pool, "ingest-first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStoreFromPoolWithLease(pool, "ingest-second", time.Minute)
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
		snapshots = append(snapshots, KnowledgeIngestSnapshot{ConversationID: bootstrap.Conversation.ID, TurnID: turn.ID, Query: "anime", Title: fmt.Sprintf("topic-%d", index), URL: fmt.Sprintf("https://example.test/%d", index), Snippet: fmt.Sprintf("这是第 %d 条足够长的知识摘要内容。", index), Rank: uint8(index%5 + 1), FetchedAtUnixMS: int64(index + 1)})
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	first, err := NewStoreFromPoolWithLease(pool, "ingest-owner-first", time.Minute)
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
	if err := first.EnqueueKnowledgeIngestSnapshotsContext(ctx, []KnowledgeIngestSnapshot{{ConversationID: bootstrap.Conversation.ID, TurnID: turn.ID, Query: "game", Title: "topic", URL: "https://example.test", Snippet: "这是一条足够长的待处理知识摘要。", Rank: 1}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	claimed, err := ClaimKnowledgeIngestJobs(ctx, pool.Raw(), 1, now, "ingest-owner-first", now+time.Minute.Milliseconds())
	if err != nil || len(claimed) != 1 {
		t.Fatalf("first claim = %#v, %v", claimed, err)
	}
	if blocked, err := ClaimKnowledgeIngestJobs(ctx, pool.Raw(), 1, now+1, "ingest-owner-second", now+1+time.Minute.Milliseconds()); err != nil || len(blocked) != 0 {
		t.Fatalf("unexpired second claim = %#v, %v", blocked, err)
	}
	if _, err := pool.Raw().Exec(ctx, "UPDATE knowledge_ingest_jobs SET lease_expires_at_ms = $2 WHERE id = $1", claimed[0].ID, now-1); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := ClaimKnowledgeIngestJobs(ctx, pool.Raw(), 1, now+2, "ingest-owner-second", now+2+time.Minute.Milliseconds())
	if err != nil || len(reclaimed) != 1 || reclaimed[0].ID != claimed[0].ID {
		t.Fatalf("reclaim = %#v, %v", reclaimed, err)
	}
	var attempts int
	var owner string
	if err := pool.Raw().QueryRow(ctx, "SELECT attempt_count, lease_owner FROM knowledge_ingest_jobs WHERE id = $1", claimed[0].ID).Scan(&attempts, &owner); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || owner != "ingest-owner-second" {
		t.Fatalf("attempts=%d owner=%q", attempts, owner)
	}
	if err := FinishKnowledgeIngestJob(ctx, pool.Raw(), claimed[0].ID, "ingest-owner-first", "succeeded", "", time.Now().UnixMilli()); err == nil {
		t.Fatal("old owner completion error = nil")
	}
	if err := FinishKnowledgeIngestJob(ctx, pool.Raw(), claimed[0].ID, "ingest-owner-second", "dropped", "", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresKnowledgeIngestDropsStructuralJunkAndValidatesLimits(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	first, err := NewStoreFromPoolWithLease(pool, "embedding-first", time.Minute)
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
		workerID string
		jobs     []EmbeddingJob
		err      error
	}
	claims := make(chan embeddingClaim, 2)
	for _, workerID := range []string{"embedding-first", "embedding-second"} {
		go func() {
			<-start
			now := time.Now().UnixMilli()
			jobs, err := ClaimEmbeddingJobs(ctx, pool.Raw(), SemanticEmbeddingModelID, SemanticEmbeddingDimensions, now, 1, workerID, now+time.Minute.Milliseconds())
			claims <- embeddingClaim{workerID: workerID, jobs: jobs, err: err}
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
	payload, err := LoadEmbeddingJobPayload(ctx, pool.Raw(), job, "personal_memory", "knowledge")
	if err != nil || payload.Content != "embedding content" {
		t.Fatalf("content = %q, %v", payload.Content, err)
	}
	if err := finishEmbeddingJobSucceeded(ctx, pool, job, otherClaim.workerID); !errors.Is(err, ErrEmbeddingJobStaleCompletion) {
		t.Fatalf("wrong owner completion error = %v", err)
	}
	if _, err := pool.Raw().Exec(ctx, "UPDATE memory_embedding_jobs SET lease_expires_at_ms = $2 WHERE id = $1", job.ID, time.Now().UnixMilli()-1); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	reclaimed, err := ClaimEmbeddingJobs(ctx, pool.Raw(), SemanticEmbeddingModelID, SemanticEmbeddingDimensions, now, 1, otherClaim.workerID, now+time.Minute.Milliseconds())
	if err != nil || len(reclaimed) != 1 || reclaimed[0].ID != job.ID {
		t.Fatalf("reclaimed = %#v, %v", reclaimed, err)
	}
	if err := finishEmbeddingJobFailed(ctx, pool, job, ownerClaim.workerID, "STALE", "old owner", false); !errors.Is(err, ErrEmbeddingJobStaleCompletion) {
		t.Fatalf("old owner failure completion error = %v", err)
	}
	if err := finishEmbeddingJobSucceeded(ctx, pool, reclaimed[0], otherClaim.workerID); err != nil {
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
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "embedding-stale", time.Minute)
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
	now := time.Now().UnixMilli()
	jobs, err := ClaimEmbeddingJobs(ctx, pool.Raw(), SemanticEmbeddingModelID, SemanticEmbeddingDimensions, now, 1, "embedding-stale", now+time.Minute.Milliseconds())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim = %#v, %v", jobs, err)
	}
	newHash := semanticContentHash("new content")
	if _, err := pool.Raw().Exec(ctx, "UPDATE memory_embedding_items SET content_hash = $2, status = 'pending', embedded_at_ms = NULL WHERE item_id = $1", created.ID, newHash); err != nil {
		t.Fatal(err)
	}
	if err := finishEmbeddingJobSucceeded(ctx, pool, jobs[0], "embedding-stale"); !errors.Is(err, ErrEmbeddingJobStaleCompletion) {
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

func finishEmbeddingJobSucceeded(ctx context.Context, pool *coredb.Pool, job EmbeddingJob, workerID string) error {
	tx, err := pool.Raw().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := FinishEmbeddingJobSucceeded(ctx, tx, job, workerID, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func finishEmbeddingJobFailed(ctx context.Context, pool *coredb.Pool, job EmbeddingJob, workerID, code, message string, retryable bool) error {
	tx, err := pool.Raw().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := FinishEmbeddingJobFailed(ctx, tx, job, workerID, code, message, retryable, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func TestPostgresTrigramRetrievalPreservesScopeLimitsAndStableOrder(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	privateOverlap, err := store.CreatePersonalMemoryContext(ctx, "experience", MemoryScope{Type: "global"}, "用户私人收藏了明年正式发布续作的纪念品", 8700)
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
	if !containsRetrievedPersonalID(knowledgeResult.PersonalMemories, privateOverlap.ID) {
		t.Fatalf("private retrieval lost overlapping personal memory = %#v", knowledgeResult.PersonalMemories)
	}
	publicResult, err := store.RetrievePublicKnowledgeContext(ctx, "明年正式发布续作")
	if err != nil {
		t.Fatal(err)
	}
	if len(publicResult.PersonalMemories) != 0 || len(publicResult.Knowledge) != 1 || publicResult.Knowledge[0].ID != knowledge.ID {
		t.Fatalf("public retrieval crossed privacy boundary = %#v", publicResult)
	}
}

func TestPostgresTrigramQueriesUseGINIndexes(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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

func explainPostgresPlan(t *testing.T, ctx context.Context, tx pgx.Tx, query string, args ...any) string {
	t.Helper()
	rows, err := tx.Query(ctx, query, args...)
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

func assertPostgresEmbeddingOutbox(t *testing.T, ctx context.Context, pool *coredb.Pool, itemKind, itemID, content string) {
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

func openIsolatedPostgresStore(t testing.TB, ctx context.Context) *coredb.Pool {
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
	pool, err := coredb.Open(ctx, coredb.ShortTimeoutConfig(withPostgresSearchPath(t, databaseURL, schema)))
	if err != nil {
		t.Fatalf("open postgres store pool: %v", err)
	}
	return pool
}

func withPostgresSearchPath(t testing.TB, rawURL string, schema string) string {
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
