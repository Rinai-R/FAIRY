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
	"unicode/utf8"

	"fairy/coredb"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresStoreSummaryUsesInjectedPool(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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

func TestPostgresPersonalMemoryLifecycleKeepsTextOnlyVectorsNull(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	assertPostgresEmbedding(t, ctx, pool, "personal_memories", created.ID, "喜欢安静", false)
	revised, err := store.RevisePersonalMemoryContext(ctx, created.ID, "更喜欢安静的环境", 9200)
	if err != nil {
		t.Fatalf("RevisePersonalMemoryContext: %v", err)
	}
	if revised.SupersedesID == nil || *revised.SupersedesID != created.ID {
		t.Fatalf("revised = %#v", revised)
	}
	assertPostgresEmbedding(t, ctx, pool, "personal_memories", revised.ID, "更喜欢安静的环境", false)
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
	assertPostgresEmbedding(t, ctx, pool, "personal_memories", legacy.ID, "旧关系记忆", false)
	assigned, err := store.AssignLegacyRelationshipContext(ctx, legacy.ID, "character-memory")
	if err != nil {
		t.Fatalf("AssignLegacyRelationshipContext: %v", err)
	}
	assertPostgresEmbedding(t, ctx, pool, "personal_memories", assigned.ID, "旧关系记忆", false)
	catalog, err := store.PersonalMemoryCatalogContext(ctx, "character-memory")
	if err != nil {
		t.Fatalf("PersonalMemoryCatalogContext: %v", err)
	}
	if len(catalog.Global) != 0 || len(catalog.Character) != 1 || catalog.Character[0].ID != assigned.ID || len(catalog.NeedsReview) != 0 {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestPostgresPersonalMemoryRollsBackWhenEmbeddingFails(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPoolWithEmbedder(pool, &fixedSemanticEmbedder{
		ready: true,
		dims:  SemanticEmbeddingDimensions,
		err:   errors.New("embedding provider failed"),
	})
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
	if _, err := store.CreatePersonalMemoryContext(ctx, "preference", MemoryScope{Type: "global"}, "must rollback", 9000); err == nil {
		t.Fatal("CreatePersonalMemoryContext error = nil, want embedding failure")
	}
	var memories int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM personal_memories WHERE content = 'must rollback'").Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if memories != 0 {
		t.Fatalf("memories=%d, want zero after rollback", memories)
	}
}

func TestPostgresPersonalMemoryContentLimitPreservesWritesAndRejectsOversizedHistory(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	assertPostgresEmbedding(t, ctx, pool, "personal_memories", created.ID, exact, false)
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
	assertPostgresEmbedding(t, ctx, pool, "personal_memories", revisedExact.ID, exact, false)
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
	assertPostgresEmbedding(t, ctx, pool, "personal_memories", exactResult[0].MemoryID, exact, false)

	_, rollbackTurnID, rollbackBatchID := seedPostgresRunningExtractionBatch(t, ctx, pool, store, "character-content-limit")
	_, err = store.CommitMemoryMutationsContext(ctx, rollbackBatchID, "character-content-limit", nil, []MemoryMutation{
		{Operation: "create", SourceTurnID: rollbackTurnID, Kind: "profile", Scope: MemoryScope{Type: "global"}, Content: "valid-before-oversized", ConfidenceBasisPoints: 8000},
		{Operation: "create", SourceTurnID: rollbackTurnID, Kind: "experience", Scope: MemoryScope{Type: "global"}, Content: tooLong, ConfidenceBasisPoints: 8000},
	})
	if err == nil || !strings.Contains(err.Error(), "2400") {
		t.Fatalf("oversized extraction error = %v", err)
	}
	var leakedMemories int
	var rollbackBatchStatus, rollbackTurnState string
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM personal_memories WHERE content = 'valid-before-oversized'").Scan(&leakedMemories); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM extraction_batches WHERE id = $1", rollbackBatchID).Scan(&rollbackBatchStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT extraction_state FROM conversation_turns WHERE id = $1", rollbackTurnID).Scan(&rollbackTurnState); err != nil {
		t.Fatal(err)
	}
	if leakedMemories != 0 || rollbackBatchStatus != "running" || rollbackTurnState != "claimed" {
		t.Fatalf("oversized extraction leaked memory=%d batch=%q turn=%q", leakedMemories, rollbackBatchStatus, rollbackTurnState)
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

func TestPostgresKnowledgeLifecyclePreservesSourcesAndTextOnlyVectors(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	assertPostgresEmbedding(t, ctx, pool, "knowledge_entries", confirmed.ID, "主题一\n候选事实", false)
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

func TestPostgresKnowledgeConfirmationRollsBackWhenEmbeddingFails(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store, err := NewStoreFromPoolWithEmbedder(pool, &fixedSemanticEmbedder{
		ready: true,
		dims:  SemanticEmbeddingDimensions,
		err:   errors.New("embedding provider failed"),
	})
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
	if _, err := store.ConfirmKnowledgeCandidateContext(ctx, "candidate-rollback"); err == nil {
		t.Fatal("ConfirmKnowledgeCandidateContext error = nil, want embedding failure")
	}
	var status, basis string
	if err := pool.Raw().QueryRow(ctx, "SELECT status, verification_basis FROM knowledge_entries WHERE id = 'candidate-rollback'").Scan(&status, &basis); err != nil {
		t.Fatal(err)
	}
	if status != "candidate" || basis != "unverified" {
		t.Fatalf("status=%q basis=%q", status, basis)
	}
}

func TestPostgresPromptWindowCommitPreservesRevisionAndCutoff(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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

func TestPostgresCommitMemoryMutationsCommitsRowsAtomically(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	assertPostgresEmbedding(t, ctx, pool, "personal_memories", results[0].MemoryID, "喜欢爵士乐", false)
	assertPostgresEmbedding(t, ctx, pool, "personal_memories", results[1].MemoryID, "愿意分享近况", false)
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	var memories int
	var batchStatus, extractionState string
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM personal_memories WHERE content = 'must rollback'").Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM extraction_batches WHERE id = $1", batchID).Scan(&batchStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT extraction_state FROM conversation_turns WHERE id = $1", turnID).Scan(&extractionState); err != nil {
		t.Fatal(err)
	}
	if memories != 0 || batchStatus != "running" || extractionState != "claimed" {
		t.Fatalf("memories=%d batch=%q turn=%q", memories, batchStatus, extractionState)
	}
}

func TestPostgresCommitMemoryMutationsPreservesNoChangeAndSupersedeSemantics(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	assertPostgresEmbedding(t, ctx, pool, "personal_memories", results[1].MemoryID, "喜欢清晨散步", false)
}

func TestPostgresCommitMemoryMutationsRejectsBatchExternalTurnAndKeepsEmptyCompletion(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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

func TestPostgresKnowledgeIngestTaskPayloadAndManagementProjection(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "batch-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-ingest-batch")
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
	sourceOne := KnowledgeIngestSource{
		ID: "web-source-1", Title: "来源一", URL: "https://one.example/item",
		Snippet: "这是第一条足够完整的公开摘要。", Rank: 1, FetchedAtUnixMS: 1,
	}
	current := KnowledgeIngestTask{
		ID: "web-task-1", ConversationID: bootstrap.Conversation.ID, TurnID: turn.ID,
		Source: sourceOne,
	}
	if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{current}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{current}); err != nil {
		t.Fatal(err)
	}
	var jobs int
	var payloadType, payloadSourceID, legacyBatchID string
	var legacySourcesUntouched bool
	if err := pool.Raw().QueryRow(ctx, `
SELECT count(*), min(jsonb_typeof(source_json)), min(source_json ->> 'id'),
       min(batch_id), bool_and(sources_json = '[]'::jsonb)
FROM knowledge_ingest_jobs
WHERE task_id = $1`, current.ID).Scan(
		&jobs, &payloadType, &payloadSourceID, &legacyBatchID, &legacySourcesUntouched,
	); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 || payloadType != "object" || payloadSourceID != sourceOne.ID ||
		legacyBatchID != "" || !legacySourcesUntouched {
		t.Fatalf(
			"jobs=%d payloadType=%q payloadSourceID=%q legacyBatchID=%q legacySourcesUntouched=%t",
			jobs, payloadType, payloadSourceID, legacyBatchID, legacySourcesUntouched,
		)
	}
	claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims = %#v, %v", claims, err)
	}
	if claims[0].Task != current {
		t.Fatalf("claimed task = %#v", claims[0])
	}
	if err := store.FailClaimedKnowledgeIngestJobContext(ctx, claims[0].JobID, "model failed"); err != nil {
		t.Fatal(err)
	}
	failed, err := store.KnowledgeIngestJobsContext(ctx, "failed")
	if err != nil || len(failed) != 1 || failed[0].TaskID != current.ID {
		t.Fatalf("failed jobs = %#v, %v", failed, err)
	}

	legacyJobID := "legacy-terminal-job"
	now := time.Now().UnixMilli()
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO knowledge_ingest_jobs(
  id, conversation_id, turn_id, batch_id, sources_json,
  status, error_category, error_message, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, 'legacy-multi-task',
          '[{"id":"legacy-1"},{"id":"legacy-2"}]'::jsonb,
          'failed', 'legacy_payload_not_singular',
          'legacy knowledge ingest payload cannot be represented as one task',
          $4, $4)`,
		legacyJobID, bootstrap.Conversation.ID, turn.ID, now,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryKnowledgeIngestJobContext(ctx, legacyJobID); err == nil ||
		!strings.Contains(err.Error(), "not retryable") {
		t.Fatalf("legacy retry error = %v", err)
	}
	var legacyStatus string
	var legacySourceCount int
	if err := pool.Raw().QueryRow(ctx, `
SELECT status, jsonb_array_length(sources_json)
FROM knowledge_ingest_jobs
WHERE id = $1`, legacyJobID).Scan(&legacyStatus, &legacySourceCount); err != nil {
		t.Fatal(err)
	}
	if legacyStatus != "failed" || legacySourceCount != 2 {
		t.Fatalf("legacy status=%q sourceCount=%d", legacyStatus, legacySourceCount)
	}
}

func TestPostgresWholeDocumentKnowledgeActionsAreAtomicAndUnstructured(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "whole-document-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, store, "character-whole-document")
	source := KnowledgeIngestSource{
		ID: "source-whole", Title: "FAIRY 项目状态", URL: "https://public.example/whole",
		Snippet: "公开项目完整状态页。", Rank: 1, FetchedAtUnixMS: 1,
	}
	claimDocument := func(batchID string, batchSource KnowledgeIngestSource, evidenceID, content string) (KnowledgeIngestClaim, KnowledgeDocument) {
		t.Helper()
		batch := KnowledgeIngestTask{
			ID: batchID, ConversationID: conversationID, TurnID: turnID,
			Source: batchSource,
		}
		if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{batch}); err != nil {
			t.Fatal(err)
		}
		claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claims=%#v err=%v", claims, err)
		}
		document := KnowledgeDocument{
			SourceID: batchSource.ID, CanonicalURL: batchSource.URL, Title: batchSource.Title,
			Content: content, ContentHash: semanticContentHash(content), EvidenceID: evidenceID,
			ContentType: "text/plain", FetchedAtUnixMS: time.Now().UnixMilli(),
		}
		needs, err := store.KnowledgeDocumentNeedsExtractionContext(
			ctx, claims[0].JobID, batchID, document,
		)
		if err != nil || !needs {
			t.Fatalf("need extraction=(%v,%v)", needs, err)
		}
		return claims[0], document
	}

	v1Text := "FAIRY 项目当前处于内部测试阶段，公开页面确认该状态。"
	v1Claim, v1Document := claimDocument("whole-batch-v1", source, "web-evidence-whole-v1", v1Text)
	add := KnowledgeDocumentAction{
		Operation: KnowledgeMutationAdd, Content: "FAIRY 项目当前处于内部测试阶段。",
		ConfidenceBasisPoints: 8500, Evidence: v1Text,
	}
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx, v1Claim.JobID, v1Claim.Task.ID, v1Document, nil, []KnowledgeDocumentAction{add},
	); err != nil {
		t.Fatal(err)
	}
	var internalID, storedBody string
	var legacyFieldsNull bool
	if err := pool.Raw().QueryRow(ctx, `
SELECT id, subject IS NULL AND predicate IS NULL AND value IS NULL AND fact_key IS NULL
FROM knowledge_entries
WHERE statement = $1 AND status = 'verified'`,
		add.Content,
	).Scan(&internalID, &legacyFieldsNull); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, `
SELECT c.text
FROM knowledge_chunks c
JOIN knowledge_document_versions v ON v.id = c.version_id
JOIN knowledge_documents d ON d.id = v.document_id
WHERE d.canonical_url = $1 AND v.status = 'current'`, source.URL).Scan(&storedBody); err != nil {
		t.Fatal(err)
	}
	if !legacyFieldsNull || storedBody != v1Text {
		t.Fatalf("legacyFieldsNull=%v storedBody=%q", legacyFieldsNull, storedBody)
	}
	recalled, err := store.SearchKnowledgeForIngestContext(ctx, "FAIRY 项目内部测试阶段", MaxKnowledgeSearchCandidates)
	if err != nil || !slices.ContainsFunc(recalled, func(item RetrievedKnowledge) bool { return item.ID == internalID }) {
		t.Fatalf("search=%#v err=%v", recalled, err)
	}

	v2Text := "FAIRY 项目已经从内部测试转为公开测试，原内部测试状态不再有效。"
	v2Claim, v2Document := claimDocument("whole-batch-v2", source, "web-evidence-whole-v2", v2Text)
	update := KnowledgeDocumentAction{
		Operation: KnowledgeMutationUpdate, MemoryID: internalID,
		Content:               "FAIRY 项目已经进入公开测试阶段，原内部测试状态已经失效。" + strings.Repeat("这是完整知识正文的补充说明。", 100),
		ConfidenceBasisPoints: 9000, Evidence: v2Text,
	}
	const legacyFactStatementLimitRunes = 1200
	if utf8.RuneCountInString(update.Content) <= legacyFactStatementLimitRunes ||
		utf8.RuneCountInString(update.Content) > maxKnowledgeDocumentActionContentRunes {
		t.Fatalf("whole-document action boundary fixture has %d runes", utf8.RuneCountInString(update.Content))
	}
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx, v2Claim.JobID, v2Claim.Task.ID, v2Document,
		[]string{internalID}, []KnowledgeDocumentAction{update},
	); err != nil {
		t.Fatal(err)
	}
	var publicID string
	if err := pool.Raw().QueryRow(ctx, `
SELECT id FROM knowledge_entries
WHERE status = 'verified' AND supersedes_id = $1 AND statement = $2
  AND subject IS NULL AND predicate IS NULL AND value IS NULL AND fact_key IS NULL`,
		internalID, update.Content,
	).Scan(&publicID); err != nil {
		t.Fatal(err)
	}
	var oldStatus string
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM knowledge_entries WHERE id = $1", internalID).Scan(&oldStatus); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "superseded" {
		t.Fatalf("old status = %q", oldStatus)
	}

	invalidText := "这次更新不得越权引用没有通过工具提供的知识 ID。"
	invalidClaim, invalidDocument := claimDocument("whole-batch-invalid", source, "web-evidence-whole-invalid", invalidText)
	_, err = store.CommitKnowledgeDocumentActionsContext(
		ctx, invalidClaim.JobID, invalidClaim.Task.ID, invalidDocument, nil,
		[]KnowledgeDocumentAction{{
			Operation: KnowledgeMutationUpdate, MemoryID: publicID,
			Content: "这条内容不应被写入数据库。", ConfidenceBasisPoints: 8000,
			Evidence: invalidText,
		}},
	)
	if err == nil {
		t.Fatal("unsupplied whole-document action error = nil")
	}
	var versionCount int
	if err := pool.Raw().QueryRow(ctx, `
SELECT count(*)
FROM knowledge_document_versions v
JOIN knowledge_documents d ON d.id = v.document_id
WHERE d.canonical_url = $1`, source.URL).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 2 {
		t.Fatalf("invalid action partially wrote version: %d", versionCount)
	}
	if err := store.FailClaimedKnowledgeIngestJobContext(ctx, invalidClaim.JobID, "expected unsupplied target"); err != nil {
		t.Fatal(err)
	}

	mirror := source
	mirror.ID = "source-whole-mirror"
	mirror.URL = "https://mirror.example/whole"
	mirrorText := "镜像公开页面同样确认 FAIRY 项目已经进入公开测试阶段。"
	mirrorClaim, mirrorDocument := claimDocument("whole-batch-mirror", mirror, "web-evidence-whole-mirror", mirrorText)
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx, mirrorClaim.JobID, mirrorClaim.Task.ID, mirrorDocument,
		[]string{publicID}, []KnowledgeDocumentAction{{
			Operation: KnowledgeMutationNone, MemoryID: publicID, Evidence: mirrorText,
		}},
	); err != nil {
		t.Fatal(err)
	}
	var activeEvidence int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM knowledge_evidence WHERE knowledge_id = $1 AND active", publicID).Scan(&activeEvidence); err != nil {
		t.Fatal(err)
	}
	if activeEvidence != 2 {
		t.Fatalf("active evidence = %d, want 2", activeEvidence)
	}

	deleteText := "官方状态页明确宣布 FAIRY 项目的公开测试已经终止，旧状态不再有效。"
	deleteClaim, deleteDocument := claimDocument("whole-batch-delete", source, "web-evidence-whole-delete", deleteText)
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx, deleteClaim.JobID, deleteClaim.Task.ID, deleteDocument,
		[]string{publicID}, []KnowledgeDocumentAction{{
			Operation: KnowledgeMutationDelete, MemoryID: publicID, Evidence: deleteText,
		}},
	); err != nil {
		t.Fatal(err)
	}
	var deletedStatus string
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM knowledge_entries WHERE id = $1", publicID).Scan(&deletedStatus); err != nil {
		t.Fatal(err)
	}
	if deletedStatus != "tombstone" {
		t.Fatalf("deleted status = %q", deletedStatus)
	}
	var deleteEvidence int
	if err := pool.Raw().QueryRow(ctx, `
SELECT count(*)
FROM knowledge_evidence
WHERE knowledge_id = $1 AND chunk_id = $2 AND active`,
		publicID, deleteDocument.EvidenceID,
	).Scan(&deleteEvidence); err != nil {
		t.Fatal(err)
	}
	if deleteEvidence != 1 {
		t.Fatalf("active DELETE evidence = %d, want 1", deleteEvidence)
	}
	recalled, err = store.SearchKnowledgeForIngestContext(ctx, "FAIRY 项目公开测试阶段", MaxKnowledgeSearchCandidates)
	if err != nil || slices.ContainsFunc(recalled, func(item RetrievedKnowledge) bool { return item.ID == publicID }) {
		t.Fatalf("deleted search=%#v err=%v", recalled, err)
	}
}

func TestPostgresWholeDocumentNoneRefreshesCanonicalSourceMetadata(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "whole-source-refresh-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, store, "character-whole-source-refresh")
	canonicalURL := "https://public.example/source-refresh"
	claim := func(batchID, sourceID, title, snippet string, rank uint8) KnowledgeIngestClaim {
		t.Helper()
		batch := KnowledgeIngestTask{
			ID: batchID, ConversationID: conversationID, TurnID: turnID,
			Source: KnowledgeIngestSource{
				ID: sourceID, Title: title, URL: canonicalURL,
				Snippet: snippet, Rank: rank, FetchedAtUnixMS: 1,
			},
		}
		if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{batch}); err != nil {
			t.Fatal(err)
		}
		claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claims=%#v err=%v", claims, err)
		}
		return claims[0]
	}
	document := func(claim KnowledgeIngestClaim, content, evidenceID string, fetchedAt int64) KnowledgeDocument {
		t.Helper()
		return KnowledgeDocument{
			SourceID: claim.Task.Source.ID, CanonicalURL: canonicalURL,
			Title:   claim.Task.Source.Title,
			Content: content, ContentHash: semanticContentHash(content), EvidenceID: evidenceID,
			ContentType: "text/plain", FetchedAtUnixMS: fetchedAt,
		}
	}

	initialClaim := claim(
		"whole-source-refresh-initial-batch",
		"whole-source-refresh-initial-source",
		"旧来源标题",
		"旧检索摘要。",
		3,
	)
	initialContent := "公开页面第一版确认 FAIRY 项目的稳定知识仍然有效。"
	initialDocument := document(
		initialClaim,
		initialContent,
		"web-evidence-whole-source-refresh-initial",
		100,
	)
	statement := "FAIRY 项目的这条稳定公开知识仍然有效。"
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx,
		initialClaim.JobID,
		initialClaim.Task.ID,
		initialDocument,
		nil,
		[]KnowledgeDocumentAction{{
			Operation:             KnowledgeMutationAdd,
			Content:               statement,
			ConfidenceBasisPoints: 8500,
			Evidence:              initialContent,
		}},
	); err != nil {
		t.Fatal(err)
	}
	var knowledgeID string
	if err := pool.Raw().QueryRow(ctx,
		"SELECT id FROM knowledge_entries WHERE statement = $1",
		statement,
	).Scan(&knowledgeID); err != nil {
		t.Fatal(err)
	}

	refreshedClaim := claim(
		"whole-source-refresh-current-batch",
		"whole-source-refresh-current-source",
		"新来源标题",
		"新检索摘要。",
		1,
	)
	refreshedContent := "公开页面第二版再次确认 FAIRY 项目的稳定知识仍然有效，并补充了来源说明。"
	refreshedDocument := document(
		refreshedClaim,
		refreshedContent,
		"web-evidence-whole-source-refresh-current",
		200,
	)
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx,
		refreshedClaim.JobID,
		refreshedClaim.Task.ID,
		refreshedDocument,
		[]string{knowledgeID},
		[]KnowledgeDocumentAction{{
			Operation: KnowledgeMutationNone,
			MemoryID:  knowledgeID,
			Evidence:  refreshedContent,
		}},
	); err != nil {
		t.Fatal(err)
	}

	var sourceCount, rank int
	var title, snippet string
	var fetchedAt int64
	if err := pool.Raw().QueryRow(ctx, `
SELECT count(*), min(title), min(snippet), min(rank), min(fetched_at_ms)
FROM knowledge_sources
WHERE knowledge_id = $1 AND canonical_url = $2`,
		knowledgeID,
		canonicalURL,
	).Scan(&sourceCount, &title, &snippet, &rank, &fetchedAt); err != nil {
		t.Fatal(err)
	}
	var activeEvidence int
	if err := pool.Raw().QueryRow(ctx,
		"SELECT count(*) FROM knowledge_evidence WHERE knowledge_id = $1 AND active",
		knowledgeID,
	).Scan(&activeEvidence); err != nil {
		t.Fatal(err)
	}
	if sourceCount != 1 || title != refreshedDocument.Title ||
		snippet != refreshedClaim.Task.Source.Snippet ||
		rank != int(refreshedClaim.Task.Source.Rank) ||
		fetchedAt != refreshedDocument.FetchedAtUnixMS ||
		activeEvidence != 1 {
		t.Fatalf(
			"canonical source count=%d title=%q snippet=%q rank=%d fetchedAt=%d activeEvidence=%d",
			sourceCount, title, snippet, rank, fetchedAt, activeEvidence,
		)
	}
}

func TestPostgresKnowledgeDocumentSameHashRefreshesCurrentSourceMetadata(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "same-hash-source-refresh-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, store, "character-same-hash-source-refresh")
	canonicalURL := "https://public.example/same-hash-source-refresh"
	claim := func(batchID, sourceID, title, snippet string, rank uint8) KnowledgeIngestClaim {
		t.Helper()
		batch := KnowledgeIngestTask{
			ID: batchID, ConversationID: conversationID, TurnID: turnID,
			Source: KnowledgeIngestSource{
				ID: sourceID, Title: title, URL: canonicalURL,
				Snippet: snippet, Rank: rank, FetchedAtUnixMS: 1,
			},
		}
		if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{batch}); err != nil {
			t.Fatal(err)
		}
		claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claims=%#v err=%v", claims, err)
		}
		return claims[0]
	}

	content := "公开页面持续确认 FAIRY 的这条稳定知识保持有效。"
	evidenceID := "web-evidence-same-hash-source-refresh"
	initialClaim := claim(
		"same-hash-source-refresh-initial-batch",
		"same-hash-source-refresh-initial-source",
		"旧页面标题",
		"旧搜索摘要。",
		3,
	)
	initialDocument := KnowledgeDocument{
		SourceID: initialClaim.Task.Source.ID, CanonicalURL: canonicalURL,
		Title:   initialClaim.Task.Source.Title,
		Content: content, ContentHash: semanticContentHash(content), EvidenceID: evidenceID,
		ContentType: "text/plain", FetchedAtUnixMS: 100,
		ETag: `"same-hash-v1"`, LastModified: "Wed, 29 Jul 2026 01:00:00 GMT",
	}
	statement := "FAIRY 的这条稳定知识保持有效。"
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx,
		initialClaim.JobID,
		initialClaim.Task.ID,
		initialDocument,
		nil,
		[]KnowledgeDocumentAction{{
			Operation: KnowledgeMutationAdd, Content: statement,
			ConfidenceBasisPoints: 8500, Evidence: content,
		}},
	); err != nil {
		t.Fatal(err)
	}
	var knowledgeID string
	if err := pool.Raw().QueryRow(ctx,
		"SELECT id FROM knowledge_entries WHERE statement = $1",
		statement,
	).Scan(&knowledgeID); err != nil {
		t.Fatal(err)
	}

	refreshedClaim := claim(
		"same-hash-source-refresh-current-batch",
		"same-hash-source-refresh-current-source",
		"新页面标题",
		"新搜索摘要。",
		1,
	)
	refreshedDocument := KnowledgeDocument{
		SourceID: refreshedClaim.Task.Source.ID, CanonicalURL: canonicalURL,
		Title:   refreshedClaim.Task.Source.Title,
		Content: content, ContentHash: semanticContentHash(content), EvidenceID: evidenceID,
		ContentType: "text/plain", FetchedAtUnixMS: 200,
		ETag: `"same-hash-v2"`, LastModified: "Wed, 29 Jul 2026 02:00:00 GMT",
	}
	needsExtraction, err := store.KnowledgeDocumentNeedsExtractionContext(
		ctx,
		refreshedClaim.JobID,
		refreshedClaim.Task.ID,
		refreshedDocument,
	)
	if err != nil || needsExtraction {
		t.Fatalf("needs extraction=(%v,%v), want (false,nil)", needsExtraction, err)
	}

	assertCurrentMetadata := func(
		wantTitle,
		wantSnippet string,
		wantRank int,
		wantFetchedAt int64,
		wantETag,
		wantLastModified string,
	) {
		t.Helper()
		var sourceCount, rank, versionCount, knowledgeCount, evidenceCount int
		var sourceTitle, snippet, documentTitle, etag, lastModified string
		var sourceFetchedAt, versionFetchedAt int64
		if err := pool.Raw().QueryRow(ctx, `
SELECT count(*), min(title), min(snippet), min(rank), min(fetched_at_ms)
FROM knowledge_sources
WHERE knowledge_id = $1 AND canonical_url = $2`,
			knowledgeID,
			canonicalURL,
		).Scan(&sourceCount, &sourceTitle, &snippet, &rank, &sourceFetchedAt); err != nil {
			t.Fatal(err)
		}
		if err := pool.Raw().QueryRow(ctx, `
SELECT d.title, count(v.id), max(v.fetched_at_ms), min(v.etag), min(v.last_modified)
FROM knowledge_documents d
JOIN knowledge_document_versions v ON v.document_id = d.id
WHERE d.canonical_url = $1
GROUP BY d.title`,
			canonicalURL,
		).Scan(&documentTitle, &versionCount, &versionFetchedAt, &etag, &lastModified); err != nil {
			t.Fatal(err)
		}
		if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM knowledge_entries").Scan(&knowledgeCount); err != nil {
			t.Fatal(err)
		}
		if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM knowledge_evidence").Scan(&evidenceCount); err != nil {
			t.Fatal(err)
		}
		if sourceCount != 1 ||
			sourceTitle != wantTitle ||
			snippet != wantSnippet ||
			rank != wantRank ||
			sourceFetchedAt != wantFetchedAt ||
			documentTitle != wantTitle ||
			versionCount != 1 ||
			versionFetchedAt != wantFetchedAt ||
			etag != wantETag ||
			lastModified != wantLastModified ||
			knowledgeCount != 1 ||
			evidenceCount != 1 {
			t.Fatalf(
				"sourceCount=%d sourceTitle=%q snippet=%q rank=%d sourceFetchedAt=%d documentTitle=%q versionCount=%d versionFetchedAt=%d etag=%q lastModified=%q knowledgeCount=%d evidenceCount=%d",
				sourceCount, sourceTitle, snippet, rank, sourceFetchedAt,
				documentTitle, versionCount, versionFetchedAt, etag, lastModified,
				knowledgeCount, evidenceCount,
			)
		}
	}
	assertCurrentMetadata(
		refreshedDocument.Title,
		refreshedClaim.Task.Source.Snippet,
		int(refreshedClaim.Task.Source.Rank),
		refreshedDocument.FetchedAtUnixMS,
		refreshedDocument.ETag,
		refreshedDocument.LastModified,
	)

	settleReplay := func(
		batchID,
		sourceID,
		title,
		snippet string,
		rank uint8,
		fetchedAt int64,
		etag,
		lastModified string,
	) {
		t.Helper()
		replayClaim := claim(batchID, sourceID, title, snippet, rank)
		replayDocument := KnowledgeDocument{
			SourceID: replayClaim.Task.Source.ID, CanonicalURL: canonicalURL,
			Title:   replayClaim.Task.Source.Title,
			Content: content, ContentHash: semanticContentHash(content), EvidenceID: evidenceID,
			ContentType: "text/plain", FetchedAtUnixMS: fetchedAt,
			ETag: etag, LastModified: lastModified,
		}
		needsExtraction, err := store.KnowledgeDocumentNeedsExtractionContext(
			ctx,
			replayClaim.JobID,
			replayClaim.Task.ID,
			replayDocument,
		)
		if err != nil || needsExtraction {
			t.Fatalf("replay needs extraction=(%v,%v), want (false,nil)", needsExtraction, err)
		}
	}
	settleReplay(
		"same-hash-source-refresh-equal-batch",
		"same-hash-source-refresh-equal-source",
		"同毫秒回退标题",
		"同毫秒回退摘要。",
		4,
		200,
		`"same-hash-equal-replay"`,
		"Wed, 29 Jul 2026 02:00:01 GMT",
	)
	settleReplay(
		"same-hash-source-refresh-older-batch",
		"same-hash-source-refresh-older-source",
		"较旧回退标题",
		"较旧回退摘要。",
		5,
		150,
		`"same-hash-older-replay"`,
		"Wed, 29 Jul 2026 01:30:00 GMT",
	)
	assertCurrentMetadata(
		refreshedDocument.Title,
		refreshedClaim.Task.Source.Snippet,
		int(refreshedClaim.Task.Source.Rank),
		refreshedDocument.FetchedAtUnixMS,
		refreshedDocument.ETag,
		refreshedDocument.LastModified,
	)
}

func TestPostgresKnowledgeDocumentSameHashDoesNotRefreshHistoricalSourceMetadata(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "same-hash-source-scope-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, store, "character-same-hash-source-scope")
	canonicalURL := "https://public.example/same-hash-source-scope"
	claimDocument := func(batchID, sourceID, title, snippet, content, evidenceID string, fetchedAt int64) (KnowledgeIngestClaim, KnowledgeDocument) {
		t.Helper()
		batch := KnowledgeIngestTask{
			ID: batchID, ConversationID: conversationID, TurnID: turnID,
			Source: KnowledgeIngestSource{
				ID: sourceID, Title: title, URL: canonicalURL,
				Snippet: snippet, Rank: 1, FetchedAtUnixMS: 1,
			},
		}
		if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{batch}); err != nil {
			t.Fatal(err)
		}
		claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claims=%#v err=%v", claims, err)
		}
		document := KnowledgeDocument{
			SourceID: claims[0].Task.Source.ID, CanonicalURL: canonicalURL,
			Title: title, Content: content, ContentHash: semanticContentHash(content),
			EvidenceID: evidenceID, ContentType: "text/plain", FetchedAtUnixMS: fetchedAt,
		}
		return claims[0], document
	}

	initialContent := "第一版公开页面确认这条历史知识仍然存在。"
	initialClaim, initialDocument := claimDocument(
		"same-hash-source-scope-initial-batch",
		"same-hash-source-scope-initial-source",
		"历史页面标题",
		"历史搜索摘要。",
		initialContent,
		"web-evidence-same-hash-source-scope-initial",
		100,
	)
	statement := "这条知识只由第一版完整正文提供证据。"
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx,
		initialClaim.JobID,
		initialClaim.Task.ID,
		initialDocument,
		nil,
		[]KnowledgeDocumentAction{{
			Operation: KnowledgeMutationAdd, Content: statement,
			ConfidenceBasisPoints: 8500, Evidence: initialContent,
		}},
	); err != nil {
		t.Fatal(err)
	}
	var knowledgeID string
	if err := pool.Raw().QueryRow(ctx,
		"SELECT id FROM knowledge_entries WHERE statement = $1",
		statement,
	).Scan(&knowledgeID); err != nil {
		t.Fatal(err)
	}

	currentContent := "第二版公开页面只包含其他内容，不再引用第一版的历史知识。"
	currentClaim, currentDocument := claimDocument(
		"same-hash-source-scope-current-batch",
		"same-hash-source-scope-current-source",
		"当前页面标题",
		"当前搜索摘要。",
		currentContent,
		"web-evidence-same-hash-source-scope-current",
		200,
	)
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx,
		currentClaim.JobID,
		currentClaim.Task.ID,
		currentDocument,
		nil,
		[]KnowledgeDocumentAction{},
	); err != nil {
		t.Fatal(err)
	}

	refreshClaim, refreshDocument := claimDocument(
		"same-hash-source-scope-refresh-batch",
		"same-hash-source-scope-refresh-source",
		"同 Hash 新页面标题",
		"同 Hash 新搜索摘要。",
		currentContent,
		currentDocument.EvidenceID,
		300,
	)
	needsExtraction, err := store.KnowledgeDocumentNeedsExtractionContext(
		ctx,
		refreshClaim.JobID,
		refreshClaim.Task.ID,
		refreshDocument,
	)
	if err != nil || needsExtraction {
		t.Fatalf("needs extraction=(%v,%v), want (false,nil)", needsExtraction, err)
	}

	var sourceTitle, sourceSnippet string
	var sourceFetchedAt int64
	if err := pool.Raw().QueryRow(ctx, `
SELECT title, snippet, fetched_at_ms
FROM knowledge_sources
WHERE knowledge_id = $1 AND canonical_url = $2`,
		knowledgeID,
		canonicalURL,
	).Scan(&sourceTitle, &sourceSnippet, &sourceFetchedAt); err != nil {
		t.Fatal(err)
	}
	if sourceTitle != initialDocument.Title ||
		sourceSnippet != initialClaim.Task.Source.Snippet ||
		sourceFetchedAt != initialDocument.FetchedAtUnixMS {
		t.Fatalf(
			"historical source title=%q snippet=%q fetchedAt=%d",
			sourceTitle,
			sourceSnippet,
			sourceFetchedAt,
		)
	}
}

func TestPostgresKnowledgeDocumentReconcilerRevisionControlsSameHashExtraction(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "reconciler-revision-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, store, "character-reconciler-revision")
	canonicalURL := "https://public.example/reconciler-revision"
	claimDocument := func(batchID, sourceID, revision string, fetchedAt int64) (KnowledgeIngestClaim, KnowledgeDocument) {
		t.Helper()
		batch := KnowledgeIngestTask{
			ID: batchID, ConversationID: conversationID, TurnID: turnID,
			Source: KnowledgeIngestSource{
				ID: sourceID, Title: "Reconciler Revision", URL: canonicalURL,
				Snippet: "同一完整正文需要在固定 reconciliation 合同升级后重新处理。", Rank: 1,
			},
		}
		if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{batch}); err != nil {
			t.Fatal(err)
		}
		claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claims=%#v err=%v", claims, err)
		}
		content := "公开页面的完整正文保持不变，但知识 reconciliation 合同已经升级。"
		return claims[0], KnowledgeDocument{
			SourceID: claims[0].Task.Source.ID, CanonicalURL: canonicalURL,
			Title: "Reconciler Revision", Content: content,
			ContentHash: semanticContentHash(content), EvidenceID: "web-evidence-reconciler-revision",
			ContentType: "text/plain", FetchedAtUnixMS: fetchedAt,
			ReconcilerRevision: revision,
		}
	}

	revisionA := strings.Repeat("a", 64)
	revisionB := strings.Repeat("b", 64)
	initialClaim, initialDocument := claimDocument(
		"reconciler-revision-initial-batch",
		"reconciler-revision-initial-source",
		revisionA,
		100,
	)
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx,
		initialClaim.JobID,
		initialClaim.Task.ID,
		initialDocument,
		nil,
		[]KnowledgeDocumentAction{},
	); err != nil {
		t.Fatal(err)
	}
	readRevisionAndCounts := func() (string, int, int) {
		t.Helper()
		var revision string
		var versions, chunks int
		if err := pool.Raw().QueryRow(ctx, `
SELECT v.reconciler_revision,
       (SELECT count(*) FROM knowledge_document_versions WHERE document_id = d.id),
       (SELECT count(*) FROM knowledge_chunks WHERE version_id = v.id)
FROM knowledge_documents AS d
JOIN knowledge_document_versions AS v ON v.id = d.current_version_id
WHERE d.canonical_url = $1`,
			canonicalURL,
		).Scan(&revision, &versions, &chunks); err != nil {
			t.Fatal(err)
		}
		return revision, versions, chunks
	}
	if revision, versions, chunks := readRevisionAndCounts(); revision != revisionA || versions != 1 || chunks != 1 {
		t.Fatalf("initial revision=%q versions=%d chunks=%d", revision, versions, chunks)
	}

	upgradedClaim, upgradedDocument := claimDocument(
		"reconciler-revision-upgraded-batch",
		"reconciler-revision-upgraded-source",
		revisionB,
		200,
	)
	needsExtraction, err := store.KnowledgeDocumentNeedsExtractionContext(
		ctx,
		upgradedClaim.JobID,
		upgradedClaim.Task.ID,
		upgradedDocument,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !needsExtraction {
		t.Fatal("same-hash reconciler revision mismatch skipped extraction")
	}
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx,
		upgradedClaim.JobID,
		upgradedClaim.Task.ID,
		upgradedDocument,
		nil,
		[]KnowledgeDocumentAction{},
	); err != nil {
		t.Fatal(err)
	}
	if revision, versions, chunks := readRevisionAndCounts(); revision != revisionB || versions != 1 || chunks != 1 {
		t.Fatalf("upgraded revision=%q versions=%d chunks=%d", revision, versions, chunks)
	}

	repeatedClaim, repeatedDocument := claimDocument(
		"reconciler-revision-repeated-batch",
		"reconciler-revision-repeated-source",
		revisionB,
		300,
	)
	needsExtraction, err = store.KnowledgeDocumentNeedsExtractionContext(
		ctx,
		repeatedClaim.JobID,
		repeatedClaim.Task.ID,
		repeatedDocument,
	)
	if err != nil || needsExtraction {
		t.Fatalf("same revision needs extraction=(%v,%v), want (false,nil)", needsExtraction, err)
	}

	failingStore, err := newStoreFromPool(pool, &fixedSemanticEmbedder{
		ready: true,
		dims:  SemanticEmbeddingDimensions,
		err:   errors.New("reconciler revision embedding failed"),
	}, "reconciler-revision-failing-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	revisionC := strings.Repeat("c", 64)
	failingBatch := KnowledgeIngestTask{
		ID: "reconciler-revision-failing-batch", ConversationID: conversationID, TurnID: turnID,
		Source: KnowledgeIngestSource{
			ID: "reconciler-revision-failing-source", Title: "Reconciler Revision",
			URL: canonicalURL, Snippet: "revision C 的失败不得推进已提交 revision。", Rank: 1,
		},
	}
	if err := failingStore.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{failingBatch}); err != nil {
		t.Fatal(err)
	}
	failingClaims, err := failingStore.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(failingClaims) != 1 {
		t.Fatalf("failing claims=%#v err=%v", failingClaims, err)
	}
	failingDocument := repeatedDocument
	failingDocument.SourceID = failingClaims[0].Task.Source.ID
	failingDocument.FetchedAtUnixMS = 400
	failingDocument.ReconcilerRevision = revisionC
	if needs, err := failingStore.KnowledgeDocumentNeedsExtractionContext(
		ctx,
		failingClaims[0].JobID,
		failingClaims[0].Task.ID,
		failingDocument,
	); err != nil || !needs {
		t.Fatalf("failing revision needs extraction=(%v,%v), want (true,nil)", needs, err)
	}
	_, err = failingStore.CommitKnowledgeDocumentActionsContext(
		ctx,
		failingClaims[0].JobID,
		failingClaims[0].Task.ID,
		failingDocument,
		nil,
		[]KnowledgeDocumentAction{{
			Operation:             KnowledgeMutationAdd,
			Content:               "Reconciler revision C 尝试新增一条稳定公开知识。",
			ConfidenceBasisPoints: 8500,
			Evidence:              failingDocument.Content,
		}},
	)
	if err == nil {
		t.Fatal("revision mismatch embedding failure error = nil")
	}
	if revision, versions, chunks := readRevisionAndCounts(); revision != revisionB || versions != 1 || chunks != 1 {
		t.Fatalf("failed revision=%q versions=%d chunks=%d", revision, versions, chunks)
	}
}

func TestPostgresKnowledgeDocumentRejectsClaimedSourceMismatchWithoutWrites(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "document-source-mismatch-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, store, "character-document-source-mismatch")
	source := KnowledgeIngestSource{
		ID: "document-source-mismatch-source", Title: "公开事实页",
		URL:     "https://public.example/document-source-mismatch",
		Snippet: "公开事实摘要。", Rank: 1, FetchedAtUnixMS: 1,
	}
	batch := KnowledgeIngestTask{
		ID: "document-source-mismatch-batch", ConversationID: conversationID, TurnID: turnID,
		Source: source,
	}
	if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{batch}); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%#v err=%v", claims, err)
	}
	content := "公开事实页包含一份完整正文，但该文档伪造了另一个来源标识。"
	document := KnowledgeDocument{
		SourceID:        "document-source-mismatch-forged",
		CanonicalURL:    source.URL,
		Title:           source.Title,
		Content:         content,
		ContentHash:     semanticContentHash(content),
		EvidenceID:      "web-evidence-document-source-mismatch",
		ContentType:     "text/plain",
		FetchedAtUnixMS: 100,
	}
	if _, err := store.KnowledgeDocumentNeedsExtractionContext(
		ctx,
		claims[0].JobID,
		claims[0].Task.ID,
		document,
	); err == nil {
		t.Fatal("source-mismatched document preflight error = nil")
	}
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx,
		claims[0].JobID,
		claims[0].Task.ID,
		document,
		nil,
		nil,
	); err == nil {
		t.Fatal("source-mismatched document commit error = nil")
	}
	var documents, versions, chunks, entries int
	if err := pool.Raw().QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM knowledge_documents),
  (SELECT count(*) FROM knowledge_document_versions),
  (SELECT count(*) FROM knowledge_chunks),
  (SELECT count(*) FROM knowledge_entries)`,
	).Scan(&documents, &versions, &chunks, &entries); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := pool.Raw().QueryRow(ctx,
		"SELECT status FROM knowledge_ingest_jobs WHERE id = $1",
		claims[0].JobID,
	).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if documents != 0 || versions != 0 || chunks != 0 || entries != 0 || status != "running" {
		t.Fatalf(
			"documents=%d versions=%d chunks=%d entries=%d status=%q",
			documents, versions, chunks, entries, status,
		)
	}
}

func TestPostgresWholeDocumentOnlyExplicitDeleteTombstonesKnowledge(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "whole-explicit-delete-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, store, "character-whole-explicit-delete")
	source := KnowledgeIngestSource{
		ID: "whole-explicit-delete-source", Title: "项目事实页",
		URL:     "https://public.example/whole-explicit-delete",
		Snippet: "项目公开事实。", Rank: 1, FetchedAtUnixMS: 1,
	}
	claimDocument := func(batchID, evidenceID, content string, fetchedAt int64) (KnowledgeIngestClaim, KnowledgeDocument) {
		t.Helper()
		batch := KnowledgeIngestTask{
			ID: batchID, ConversationID: conversationID, TurnID: turnID,
			Source: source,
		}
		if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{batch}); err != nil {
			t.Fatal(err)
		}
		claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claims=%#v err=%v", claims, err)
		}
		return claims[0], KnowledgeDocument{
			SourceID: source.ID, CanonicalURL: source.URL, Title: source.Title,
			Content: content, ContentHash: semanticContentHash(content), EvidenceID: evidenceID,
			ContentType: "text/plain", FetchedAtUnixMS: fetchedAt,
		}
	}

	retractedStatement := "FAIRY 项目公开提供旧版下载。"
	omittedStatement := "FAIRY 项目的稳定版支持离线运行。"
	initialRetractedEvidence := "项目事实页确认 FAIRY 项目公开提供旧版下载。"
	initialOmittedEvidence := "项目事实页确认 FAIRY 项目的稳定版支持离线运行。"
	initialContent := initialRetractedEvidence + "\n" + initialOmittedEvidence
	initialClaim, initialDocument := claimDocument(
		"whole-explicit-delete-initial-batch",
		"web-evidence-whole-explicit-delete-initial",
		initialContent,
		100,
	)
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx,
		initialClaim.JobID,
		initialClaim.Task.ID,
		initialDocument,
		nil,
		[]KnowledgeDocumentAction{
			{
				Operation: KnowledgeMutationAdd, Content: retractedStatement,
				ConfidenceBasisPoints: 8500, Evidence: initialRetractedEvidence,
			},
			{
				Operation: KnowledgeMutationAdd, Content: omittedStatement,
				ConfidenceBasisPoints: 8500, Evidence: initialOmittedEvidence,
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	knowledgeIDs := make(map[string]string)
	rows, err := pool.Raw().Query(ctx, `
SELECT statement, id
FROM knowledge_entries
WHERE statement = ANY($1)`,
		[]string{retractedStatement, omittedStatement},
	)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var statement, id string
		if err := rows.Scan(&statement, &id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		knowledgeIDs[statement] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(knowledgeIDs) != 2 {
		t.Fatalf("knowledge IDs = %#v", knowledgeIDs)
	}

	retractionEvidence := "项目事实页明确撤回旧版下载，旧下载不再提供。"
	currentContent := retractionEvidence + "\n本版页面只发布当前变更，不重复列举其他稳定能力。"
	currentClaim, currentDocument := claimDocument(
		"whole-explicit-delete-current-batch",
		"web-evidence-whole-explicit-delete-current",
		currentContent,
		200,
	)
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx,
		currentClaim.JobID,
		currentClaim.Task.ID,
		currentDocument,
		[]string{knowledgeIDs[retractedStatement]},
		[]KnowledgeDocumentAction{{
			Operation: KnowledgeMutationDelete,
			MemoryID:  knowledgeIDs[retractedStatement],
			Evidence:  retractionEvidence,
		}},
	); err != nil {
		t.Fatal(err)
	}

	var retractedStatus, omittedStatus string
	if err := pool.Raw().QueryRow(ctx,
		"SELECT status FROM knowledge_entries WHERE id = $1",
		knowledgeIDs[retractedStatement],
	).Scan(&retractedStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx,
		"SELECT status FROM knowledge_entries WHERE id = $1",
		knowledgeIDs[omittedStatement],
	).Scan(&omittedStatus); err != nil {
		t.Fatal(err)
	}
	var omittedActiveEvidence int
	if err := pool.Raw().QueryRow(ctx,
		"SELECT count(*) FROM knowledge_evidence WHERE knowledge_id = $1 AND active",
		knowledgeIDs[omittedStatement],
	).Scan(&omittedActiveEvidence); err != nil {
		t.Fatal(err)
	}
	if retractedStatus != "tombstone" ||
		omittedStatus != "verified" ||
		omittedActiveEvidence != 1 {
		t.Fatalf(
			"retractedStatus=%q omittedStatus=%q omittedActiveEvidence=%d",
			retractedStatus, omittedStatus, omittedActiveEvidence,
		)
	}

	emptyActionContent := "项目事实页本版只发布维护窗口，没有可沉淀的稳定公开知识。"
	emptyActionClaim, emptyActionDocument := claimDocument(
		"whole-explicit-delete-empty-action-batch",
		"web-evidence-whole-explicit-delete-empty-action",
		emptyActionContent,
		300,
	)
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx,
		emptyActionClaim.JobID,
		emptyActionClaim.Task.ID,
		emptyActionDocument,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	var currentHash string
	if err := pool.Raw().QueryRow(ctx,
		"SELECT current_content_hash FROM knowledge_documents WHERE canonical_url = $1",
		source.URL,
	).Scan(&currentHash); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx,
		"SELECT status FROM knowledge_entries WHERE id = $1",
		knowledgeIDs[omittedStatement],
	).Scan(&omittedStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx,
		"SELECT count(*) FROM knowledge_evidence WHERE knowledge_id = $1 AND active",
		knowledgeIDs[omittedStatement],
	).Scan(&omittedActiveEvidence); err != nil {
		t.Fatal(err)
	}
	if currentHash != emptyActionDocument.ContentHash ||
		omittedStatus != "verified" ||
		omittedActiveEvidence != 1 {
		t.Fatalf(
			"currentHash=%q omittedStatus=%q omittedActiveEvidence=%d",
			currentHash, omittedStatus, omittedActiveEvidence,
		)
	}

	reconfirmedEvidence := "项目事实页再次确认 FAIRY 项目的稳定版支持离线运行。"
	reconfirmedClaim, reconfirmedDocument := claimDocument(
		"whole-explicit-delete-reconfirmed-batch",
		"web-evidence-whole-explicit-delete-reconfirmed",
		reconfirmedEvidence,
		400,
	)
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx,
		reconfirmedClaim.JobID,
		reconfirmedClaim.Task.ID,
		reconfirmedDocument,
		[]string{knowledgeIDs[omittedStatement]},
		[]KnowledgeDocumentAction{{
			Operation: KnowledgeMutationNone,
			MemoryID:  knowledgeIDs[omittedStatement],
			Evidence:  reconfirmedEvidence,
		}},
	); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx,
		"SELECT count(*) FROM knowledge_evidence WHERE knowledge_id = $1 AND active",
		knowledgeIDs[omittedStatement],
	).Scan(&omittedActiveEvidence); err != nil {
		t.Fatal(err)
	}
	if omittedActiveEvidence != 1 {
		t.Fatalf("reconfirmed active evidence = %d, want 1", omittedActiveEvidence)
	}
}

func TestPostgresKnowledgeDocumentCommitDoesNotTombstoneUnrelatedInactiveKnowledge(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "unrelated-tombstone-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, store, "character-unrelated-tombstone")
	commit := func(batchID string, source KnowledgeIngestSource, document KnowledgeDocument, actions []KnowledgeDocumentAction) {
		t.Helper()
		batch := KnowledgeIngestTask{
			ID: batchID, ConversationID: conversationID, TurnID: turnID,
			Source: source,
		}
		if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{batch}); err != nil {
			t.Fatal(err)
		}
		claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claims=%#v err=%v", claims, err)
		}
		document.SourceID = source.ID
		document.CanonicalURL = source.URL
		document.Title = source.Title
		if _, err := store.CommitKnowledgeDocumentActionsContext(
			ctx,
			claims[0].JobID,
			batch.ID,
			document,
			nil,
			actions,
		); err != nil {
			t.Fatal(err)
		}
	}

	legacySource := KnowledgeIngestSource{
		ID: "unrelated-tombstone-legacy-source", Title: "历史来源",
		URL:     "https://public.example/unrelated-tombstone-legacy",
		Snippet: "历史公开知识。", Rank: 1, FetchedAtUnixMS: 1,
	}
	legacyContent := "历史来源确认这条知识属于独立的历史公开记录。"
	legacyStatement := "这是一条独立的历史公开知识。"
	commit(
		"unrelated-tombstone-legacy-batch",
		legacySource,
		KnowledgeDocument{
			Content: legacyContent, ContentHash: semanticContentHash(legacyContent),
			EvidenceID:  "web-evidence-unrelated-tombstone-legacy",
			ContentType: "text/plain", FetchedAtUnixMS: 100,
		},
		[]KnowledgeDocumentAction{{
			Operation: KnowledgeMutationAdd, Content: legacyStatement,
			ConfidenceBasisPoints: 8500, Evidence: legacyContent,
		}},
	)
	var legacyKnowledgeID string
	if err := pool.Raw().QueryRow(ctx,
		"SELECT id FROM knowledge_entries WHERE statement = $1",
		legacyStatement,
	).Scan(&legacyKnowledgeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Raw().Exec(ctx, `
UPDATE knowledge_evidence
SET active = false, invalidated_at_ms = $2
WHERE knowledge_id = $1`,
		legacyKnowledgeID,
		nowUnixMS(),
	); err != nil {
		t.Fatal(err)
	}

	unrelatedSource := KnowledgeIngestSource{
		ID: "unrelated-tombstone-current-source", Title: "无关来源",
		URL:     "https://public.example/unrelated-tombstone-current",
		Snippet: "无关页面。", Rank: 1, FetchedAtUnixMS: 2,
	}
	unrelatedContent := "这个无关页面本次没有任何需要沉淀的稳定公开知识。"
	commit(
		"unrelated-tombstone-current-batch",
		unrelatedSource,
		KnowledgeDocument{
			Content: unrelatedContent, ContentHash: semanticContentHash(unrelatedContent),
			EvidenceID:  "web-evidence-unrelated-tombstone-current",
			ContentType: "text/plain", FetchedAtUnixMS: 200,
		},
		nil,
	)

	var status string
	if err := pool.Raw().QueryRow(ctx,
		"SELECT status FROM knowledge_entries WHERE id = $1",
		legacyKnowledgeID,
	).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "verified" {
		t.Fatalf("unrelated inactive knowledge status = %q, want verified", status)
	}
}

func TestPostgresCanonicalKnowledgeSourceMergeIsFreshnessAwareAndLegacyCompatible(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, store, "character-source-merge")
	knowledgeID := "knowledge-source-merge"
	tx, err := pool.Raw().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := InsertVerifiedKnowledgeEntry(
		ctx,
		tx,
		knowledgeID,
		"来源合并",
		"来源元数据按抓取时间单调合并。",
		conversationID,
		turnID,
		8500,
		1,
		EmbeddingValue{},
	); err != nil {
		t.Fatal(err)
	}

	canonicalURL := "https://public.example/canonical-source-merge"
	currentSource := AssistantSource{
		Title: "当前标题", URL: canonicalURL, Snippet: "当前摘要。",
		Rank: 1, FetchedAtUnixMS: 200,
	}
	for index, source := range []AssistantSource{
		currentSource,
		{
			Title: "较旧标题", URL: canonicalURL, Snippet: "较旧摘要。",
			Rank: 4, FetchedAtUnixMS: 150,
		},
		{
			Title: "同毫秒标题", URL: canonicalURL, Snippet: "同毫秒摘要。",
			Rank: 5, FetchedAtUnixMS: 200,
		},
	} {
		if err := InsertCanonicalKnowledgeSource(
			ctx,
			tx,
			knowledgeID,
			fmt.Sprintf("canonical-source-%d", index),
			source,
		); err != nil {
			t.Fatal(err)
		}
	}

	legacyURL := "https://public.example/legacy-source-merge"
	if _, err := tx.Exec(ctx, `
INSERT INTO knowledge_sources(
  knowledge_id, source_id, title, url, canonical_url, snippet, rank, fetched_at_ms
) VALUES ($1, 'legacy-source', '旧 Legacy 标题', $2, '', '旧 Legacy 摘要。', 3, 50)`,
		knowledgeID,
		legacyURL,
	); err != nil {
		t.Fatal(err)
	}
	legacyCurrentSource := AssistantSource{
		Title: "新 Legacy 标题", URL: legacyURL, Snippet: "新 Legacy 摘要。",
		Rank: 2, FetchedAtUnixMS: 100,
	}
	if err := InsertCanonicalKnowledgeSource(
		ctx,
		tx,
		knowledgeID,
		"canonical-source-legacy-refresh",
		legacyCurrentSource,
	); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	assertSource := func(url string, want AssistantSource, wantCanonicalURL string) {
		t.Helper()
		var count, rank int
		var title, snippet, canonicalURL string
		var fetchedAt int64
		if err := pool.Raw().QueryRow(ctx, `
SELECT count(*), min(title), min(snippet), min(rank), min(fetched_at_ms), min(canonical_url)
FROM knowledge_sources
WHERE knowledge_id = $1
  AND (canonical_url = $2 OR url = $2)`,
			knowledgeID,
			url,
		).Scan(&count, &title, &snippet, &rank, &fetchedAt, &canonicalURL); err != nil {
			t.Fatal(err)
		}
		if count != 1 ||
			title != want.Title ||
			snippet != want.Snippet ||
			rank != int(want.Rank) ||
			fetchedAt != want.FetchedAtUnixMS ||
			canonicalURL != wantCanonicalURL {
			t.Fatalf(
				"source %q count=%d title=%q snippet=%q rank=%d fetchedAt=%d canonicalURL=%q",
				url, count, title, snippet, rank, fetchedAt, canonicalURL,
			)
		}
	}
	assertSource(canonicalURL, currentSource, canonicalURL)
	assertSource(legacyURL, legacyCurrentSource, "")
}

func TestPostgresWholeDocumentRevertAddsCompleteEvidenceToLegacyVersion(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "whole-revert-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, store, "character-whole-revert")
	source := KnowledgeIngestSource{
		ID: "source-whole-revert", Title: "版本状态页", URL: "https://public.example/revert",
		Snippet: "公开版本状态。", Rank: 1, FetchedAtUnixMS: 1,
	}
	claim := func(batchID string) KnowledgeIngestClaim {
		t.Helper()
		batch := KnowledgeIngestTask{
			ID: batchID, ConversationID: conversationID, TurnID: turnID,
			Source: source,
		}
		if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{batch}); err != nil {
			t.Fatal(err)
		}
		claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claims=%#v err=%v", claims, err)
		}
		return claims[0]
	}

	v1Text := "项目版本一完整正文已经公开并保持有效。"
	legacyDocumentID := "legacy-document-whole-revert"
	legacyVersionID := "legacy-version-whole-revert"
	legacyChunks := []struct {
		id   string
		text string
	}{
		{id: "legacy-chunk-whole-revert-0", text: "项目版本一完整正文"},
		{id: "legacy-chunk-whole-revert-1", text: "已经公开并保持有效。"},
	}
	tx, err := pool.Raw().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
INSERT INTO knowledge_documents(
  id, canonical_url, title, current_version_id, current_content_hash,
  created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, NULL, NULL, 1, 1)`,
		legacyDocumentID, source.URL, source.Title,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO knowledge_document_versions(
  id, document_id, content_hash, content_type, status, fetched_at_ms,
  etag, last_modified, reconciler_revision, created_at_ms
) VALUES ($1, $2, $3, 'text/plain', 'current', 1, '', '', '', 1)`,
		legacyVersionID, legacyDocumentID, semanticContentHash(v1Text),
	); err != nil {
		t.Fatal(err)
	}
	for ordinal, chunk := range legacyChunks {
		if _, err := tx.Exec(ctx, `
INSERT INTO knowledge_chunks(
  id, version_id, ordinal, text, text_hash, created_at_ms
) VALUES ($1, $2, $3, $4, $5, 1)`,
			chunk.id, legacyVersionID, ordinal, chunk.text, semanticContentHash(chunk.text),
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `
UPDATE knowledge_documents
SET current_version_id = $2, current_content_hash = $3
WHERE id = $1`,
		legacyDocumentID, legacyVersionID, semanticContentHash(v1Text),
	); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	v2Text := "项目版本二完整正文已经公开并替代版本一。"
	v2Claim := claim("whole-revert-v2")
	v2 := KnowledgeDocument{
		SourceID: source.ID, CanonicalURL: source.URL, Title: source.Title,
		Content: v2Text, ContentHash: semanticContentHash(v2Text),
		EvidenceID:  "web-evidence-whole-revert-v2",
		ContentType: "text/plain", FetchedAtUnixMS: 2,
	}
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx, v2Claim.JobID, v2Claim.Task.ID, v2, nil, nil,
	); err != nil {
		t.Fatal(err)
	}

	revertClaim := claim("whole-revert-v1-current")
	reverted := KnowledgeDocument{
		SourceID: source.ID, CanonicalURL: source.URL, Title: source.Title,
		Content: v1Text, ContentHash: semanticContentHash(v1Text),
		EvidenceID:  "web-evidence-whole-revert-v1",
		ContentType: "text/plain", FetchedAtUnixMS: 3,
	}
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx, revertClaim.JobID, revertClaim.Task.ID, reverted, nil,
		[]KnowledgeDocumentAction{{
			Operation:             KnowledgeMutationAdd,
			Content:               "项目当前已经恢复到版本一，版本一状态重新有效。",
			ConfidenceBasisPoints: 8500,
			Evidence:              v1Text,
		}},
	); err != nil {
		t.Fatal(err)
	}
	var ordinal int
	var storedText string
	if err := pool.Raw().QueryRow(ctx, `
SELECT ordinal, text
FROM knowledge_chunks
WHERE id = $1`, reverted.EvidenceID).Scan(&ordinal, &storedText); err != nil {
		t.Fatal(err)
	}
	if ordinal != len(legacyChunks) || storedText != v1Text {
		t.Fatalf("complete legacy evidence ordinal=%d text=%q", ordinal, storedText)
	}
	var legacyChunkCount int
	if err := pool.Raw().QueryRow(ctx, `
SELECT count(*)
FROM knowledge_chunks
WHERE version_id = $1
  AND id = ANY($2::text[])`,
		legacyVersionID,
		[]string{legacyChunks[0].id, legacyChunks[1].id},
	).Scan(&legacyChunkCount); err != nil {
		t.Fatal(err)
	}
	if legacyChunkCount != len(legacyChunks) {
		t.Fatalf("legacy chunks preserved=%d, want %d", legacyChunkCount, len(legacyChunks))
	}
	var activeEvidence int
	if err := pool.Raw().QueryRow(ctx, `
SELECT count(*)
FROM knowledge_evidence
WHERE chunk_id = $1 AND active`, reverted.EvidenceID).Scan(&activeEvidence); err != nil {
		t.Fatal(err)
	}
	if activeEvidence != 1 {
		t.Fatalf("reverted complete evidence = %d, want 1", activeEvidence)
	}
}

func TestPostgresKnowledgeDocumentOutOfOrderCommitKeepsNewestFetchCurrent(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	first, err := NewStoreFromPoolWithLease(pool, "document-order-first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStoreFromPoolWithLease(pool, "document-order-second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, first, "character-document-order")
	canonicalURL := "https://public.example/document-order"
	batches := []KnowledgeIngestTask{
		{
			ID: "document-order-batch-a", ConversationID: conversationID, TurnID: turnID,
			Source: KnowledgeIngestSource{
				ID: "document-order-source-a", Title: "版本来源", URL: canonicalURL,
				Snippet: "第一个并发任务。", Rank: 1, FetchedAtUnixMS: 1,
			},
		},
		{
			ID: "document-order-batch-b", ConversationID: conversationID, TurnID: turnID,
			Source: KnowledgeIngestSource{
				ID: "document-order-source-b", Title: "版本来源", URL: canonicalURL,
				Snippet: "第二个并发任务。", Rank: 1, FetchedAtUnixMS: 2,
			},
		},
	}
	if err := first.EnqueueKnowledgeIngestTasksContext(ctx, batches); err != nil {
		t.Fatal(err)
	}
	olderClaims, err := first.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(olderClaims) != 1 {
		t.Fatalf("older claims=%#v err=%v", olderClaims, err)
	}
	newerClaims, err := second.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(newerClaims) != 1 {
		t.Fatalf("newer claims=%#v err=%v", newerClaims, err)
	}

	document := func(claim KnowledgeIngestClaim, content, evidenceID string, fetchedAt int64) KnowledgeDocument {
		return KnowledgeDocument{
			SourceID: claim.Task.Source.ID, CanonicalURL: canonicalURL, Title: "版本来源",
			Content: content, ContentHash: semanticContentHash(content), EvidenceID: evidenceID,
			ContentType: "text/plain", FetchedAtUnixMS: fetchedAt,
		}
	}
	older := document(
		olderClaims[0],
		"这是较早抓取的完整页面正文，慢模型不应覆盖较新的页面版本。",
		"web-evidence-document-order-old",
		100,
	)
	newer := document(
		newerClaims[0],
		"这是较晚抓取的完整页面正文，应当保持为该 URL 的当前版本。",
		"web-evidence-document-order-new",
		200,
	)
	if needs, err := first.KnowledgeDocumentNeedsExtractionContext(
		ctx, olderClaims[0].JobID, olderClaims[0].Task.ID, older,
	); err != nil || !needs {
		t.Fatalf("older needs extraction=%v err=%v", needs, err)
	}
	if needs, err := second.KnowledgeDocumentNeedsExtractionContext(
		ctx, newerClaims[0].JobID, newerClaims[0].Task.ID, newer,
	); err != nil || !needs {
		t.Fatalf("newer needs extraction=%v err=%v", needs, err)
	}
	if _, err := second.CommitKnowledgeDocumentActionsContext(
		ctx, newerClaims[0].JobID, newerClaims[0].Task.ID, newer, nil, []KnowledgeDocumentAction{},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CommitKnowledgeDocumentActionsContext(
		ctx, olderClaims[0].JobID, olderClaims[0].Task.ID, older, nil, []KnowledgeDocumentAction{},
	); err != nil {
		t.Fatal(err)
	}

	var currentHash string
	var currentFetchedAt int64
	var versionCount int
	var olderStatus string
	if err := pool.Raw().QueryRow(ctx, `
SELECT d.current_content_hash, v.fetched_at_ms
FROM knowledge_documents d
JOIN knowledge_document_versions v ON v.id = d.current_version_id
WHERE d.canonical_url = $1`, canonicalURL).Scan(&currentHash, &currentFetchedAt); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, `
SELECT count(*)
FROM knowledge_document_versions v
JOIN knowledge_documents d ON d.id = v.document_id
WHERE d.canonical_url = $1`, canonicalURL).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx,
		"SELECT status FROM knowledge_ingest_jobs WHERE id = $1",
		olderClaims[0].JobID,
	).Scan(&olderStatus); err != nil {
		t.Fatal(err)
	}
	if currentHash != newer.ContentHash || currentFetchedAt != newer.FetchedAtUnixMS ||
		versionCount != 1 || olderStatus != "succeeded" {
		t.Fatalf(
			"out-of-order current hash=%q fetchedAt=%d versions=%d olderStatus=%q, want hash=%q fetchedAt=%d versions=1 status=succeeded",
			currentHash, currentFetchedAt, versionCount, olderStatus, newer.ContentHash, newer.FetchedAtUnixMS,
		)
	}
}

func TestPostgresStaleKnowledgeDocumentSkipsEmbeddingBeforeCommit(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	newerStore, err := NewStoreFromPoolWithLease(pool, "stale-embedding-newer", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	embedder := &fixedSemanticEmbedder{
		ready: true,
		dims:  SemanticEmbeddingDimensions,
		err:   errors.New("stale document must not call embedding provider"),
	}
	olderStore, err := newStoreFromPool(pool, embedder, "stale-embedding-older", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, newerStore, "character-stale-embedding")
	canonicalURL := "https://public.example/stale-embedding"
	if err := newerStore.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{
		{
			ID: "stale-embedding-batch-newer", ConversationID: conversationID, TurnID: turnID,
			Source: KnowledgeIngestSource{
				ID: "stale-embedding-source-newer", Title: "新版本", URL: canonicalURL,
				Snippet: "较新抓取。", Rank: 1, FetchedAtUnixMS: 2,
			},
		},
		{
			ID: "stale-embedding-batch-older", ConversationID: conversationID, TurnID: turnID,
			Source: KnowledgeIngestSource{
				ID: "stale-embedding-source-older", Title: "旧版本", URL: canonicalURL,
				Snippet: "较早抓取。", Rank: 1, FetchedAtUnixMS: 1,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	newerClaims, err := newerStore.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(newerClaims) != 1 {
		t.Fatalf("newer claims=%#v err=%v", newerClaims, err)
	}
	olderClaims, err := olderStore.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(olderClaims) != 1 {
		t.Fatalf("older claims=%#v err=%v", olderClaims, err)
	}
	document := func(claim KnowledgeIngestClaim, title, content, evidenceID string, fetchedAt int64) KnowledgeDocument {
		t.Helper()
		return KnowledgeDocument{
			SourceID: claim.Task.Source.ID, CanonicalURL: canonicalURL, Title: title,
			Content: content, ContentHash: semanticContentHash(content), EvidenceID: evidenceID,
			ContentType: "text/plain", FetchedAtUnixMS: fetchedAt,
		}
	}
	newer := document(
		newerClaims[0],
		"新版本",
		"较新的完整页面正文已经成为 current，旧任务不得继续执行 action。",
		"web-evidence-stale-embedding-newer",
		200,
	)
	older := document(
		olderClaims[0],
		"旧版本",
		"较早的完整页面正文包含一条会触发 ADD embedding 的候选知识。",
		"web-evidence-stale-embedding-older",
		100,
	)
	if needs, err := newerStore.KnowledgeDocumentNeedsExtractionContext(
		ctx, newerClaims[0].JobID, newerClaims[0].Task.ID, newer,
	); err != nil || !needs {
		t.Fatalf("newer needs extraction=%v err=%v", needs, err)
	}
	if needs, err := olderStore.KnowledgeDocumentNeedsExtractionContext(
		ctx, olderClaims[0].JobID, olderClaims[0].Task.ID, older,
	); err != nil || !needs {
		t.Fatalf("older needs extraction=%v err=%v", needs, err)
	}
	if _, err := newerStore.CommitKnowledgeDocumentActionsContext(
		ctx, newerClaims[0].JobID, newerClaims[0].Task.ID, newer, nil, []KnowledgeDocumentAction{},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := olderStore.CommitKnowledgeDocumentActionsContext(
		ctx,
		olderClaims[0].JobID,
		olderClaims[0].Task.ID,
		older,
		nil,
		[]KnowledgeDocumentAction{{
			Operation:             KnowledgeMutationAdd,
			Content:               "这条旧知识不应进入 embedding 或 PostgreSQL。",
			ConfidenceBasisPoints: 8000,
			Evidence:              "较早的完整页面正文包含一条会触发 ADD embedding 的候选知识。",
		}},
	); err != nil {
		t.Fatal(err)
	}
	if len(embedder.inputs) != 0 {
		t.Fatalf("stale document called embedding provider with %#v", embedder.inputs)
	}
	var versions, knowledge int
	var olderStatus string
	if err := pool.Raw().QueryRow(ctx, `
SELECT count(*)
FROM knowledge_document_versions v
JOIN knowledge_documents d ON d.id = v.document_id
WHERE d.canonical_url = $1`, canonicalURL).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx,
		"SELECT count(*) FROM knowledge_entries WHERE statement = $1",
		"这条旧知识不应进入 embedding 或 PostgreSQL。",
	).Scan(&knowledge); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx,
		"SELECT status FROM knowledge_ingest_jobs WHERE id = $1",
		olderClaims[0].JobID,
	).Scan(&olderStatus); err != nil {
		t.Fatal(err)
	}
	if versions != 1 || knowledge != 0 || olderStatus != "succeeded" {
		t.Fatalf("stale action versions=%d knowledge=%d status=%q", versions, knowledge, olderStatus)
	}
}

func TestPostgresFreshKnowledgeDocumentEmbedsBeforeCommit(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	vector := make([]float32, SemanticEmbeddingDimensions)
	vector[0] = 1
	embedder := &fixedSemanticEmbedder{
		ready: true, dims: SemanticEmbeddingDimensions,
		vectors: [][]float32{vector},
	}
	store, err := newStoreFromPool(pool, embedder, "fresh-embedding-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, store, "character-fresh-embedding")
	canonicalURL := "https://public.example/fresh-embedding"
	initialBatch := KnowledgeIngestTask{
		ID: "fresh-embedding-initial-batch", ConversationID: conversationID, TurnID: turnID,
		Source: KnowledgeIngestSource{
			ID: "fresh-embedding-initial-source", Title: "旧页面", URL: canonicalURL,
			Snippet: "旧的公开页面。", Rank: 1, FetchedAtUnixMS: 1,
		},
	}
	if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{initialBatch}); err != nil {
		t.Fatal(err)
	}
	initialClaims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(initialClaims) != 1 {
		t.Fatalf("initial claims=%#v err=%v", initialClaims, err)
	}
	initialContent := "旧的完整页面正文先成为 current，后续严格更新才应调用 embedding。"
	initialDocument := KnowledgeDocument{
		SourceID: initialBatch.Source.ID, CanonicalURL: canonicalURL, Title: initialBatch.Source.Title,
		Content: initialContent, ContentHash: semanticContentHash(initialContent), EvidenceID: "web-evidence-fresh-embedding-initial",
		ContentType: "text/plain", FetchedAtUnixMS: 50,
	}
	if _, err := store.CommitKnowledgeDocumentActionsContext(
		ctx,
		initialClaims[0].JobID,
		initialClaims[0].Task.ID,
		initialDocument,
		nil,
		[]KnowledgeDocumentAction{},
	); err != nil {
		t.Fatal(err)
	}
	batch := KnowledgeIngestTask{
		ID: "fresh-embedding-batch", ConversationID: conversationID, TurnID: turnID,
		Source: KnowledgeIngestSource{
			ID: "fresh-embedding-source", Title: "新页面", URL: canonicalURL,
			Snippet: "新的公开页面。", Rank: 1, FetchedAtUnixMS: 1,
		},
	}
	if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{batch}); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%#v err=%v", claims, err)
	}
	content := "新的完整页面正文包含一条需要向量化后写入的知识。"
	document := KnowledgeDocument{
		SourceID: batch.Source.ID, CanonicalURL: batch.Source.URL, Title: batch.Source.Title,
		Content: content, ContentHash: semanticContentHash(content), EvidenceID: "web-evidence-fresh-embedding",
		ContentType: "text/plain", FetchedAtUnixMS: 100,
	}
	if needs, err := store.KnowledgeDocumentNeedsExtractionContext(
		ctx, claims[0].JobID, claims[0].Task.ID, document,
	); err != nil || !needs {
		t.Fatalf("needs extraction=%v err=%v", needs, err)
	}
	statement := "这条新知识必须先成功生成向量再写入 PostgreSQL。"
	written, err := store.CommitKnowledgeDocumentActionsContext(
		ctx,
		claims[0].JobID,
		claims[0].Task.ID,
		document,
		nil,
		[]KnowledgeDocumentAction{{
			Operation:             KnowledgeMutationAdd,
			Content:               statement,
			ConfidenceBasisPoints: 8500,
			Evidence:              content,
		}},
	)
	if err != nil || written != 1 {
		t.Fatalf("written=%d err=%v", written, err)
	}
	if len(embedder.inputs) != 1 || len(embedder.inputs[0]) != 1 {
		t.Fatalf("embedding inputs=%#v", embedder.inputs)
	}
	var embedded bool
	var status string
	if err := pool.Raw().QueryRow(ctx,
		"SELECT embedding IS NOT NULL FROM knowledge_entries WHERE statement = $1",
		statement,
	).Scan(&embedded); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx,
		"SELECT status FROM knowledge_ingest_jobs WHERE id = $1",
		claims[0].JobID,
	).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if !embedded || status != "succeeded" {
		t.Fatalf("embedded=%v status=%q", embedded, status)
	}
}

type blockingKnowledgeDocumentEmbedder struct {
	started chan struct{}
	release <-chan struct{}
	vector  []float32
	inputs  [][]string
}

func (embedder *blockingKnowledgeDocumentEmbedder) Ready() bool {
	return true
}

func (embedder *blockingKnowledgeDocumentEmbedder) Status() SemanticStatus {
	return SemanticStatusReady
}

func (embedder *blockingKnowledgeDocumentEmbedder) Dims() int {
	return SemanticEmbeddingDimensions
}

func (embedder *blockingKnowledgeDocumentEmbedder) Embed(texts []string) ([][]float32, error) {
	embedder.inputs = append(embedder.inputs, append([]string(nil), texts...))
	close(embedder.started)
	<-embedder.release
	return [][]float32{embedder.vector}, nil
}

func TestPostgresKnowledgeDocumentRechecksFreshnessAfterEmbedding(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	vector := make([]float32, SemanticEmbeddingDimensions)
	vector[0] = 1
	embedder := &blockingKnowledgeDocumentEmbedder{
		started: started,
		release: release,
		vector:  vector,
	}
	olderStore, err := newStoreFromPool(pool, embedder, "embedding-race-older", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	newerStore, err := NewStoreFromPoolWithLease(pool, "embedding-race-newer", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, newerStore, "character-embedding-race")
	canonicalURL := "https://public.example/embedding-race"
	if err := newerStore.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{
		{
			ID: "embedding-race-batch-older", ConversationID: conversationID, TurnID: turnID,
			Source: KnowledgeIngestSource{
				ID: "embedding-race-source-older", Title: "旧版本", URL: canonicalURL,
				Snippet: "较早抓取。", Rank: 1, FetchedAtUnixMS: 1,
			},
		},
		{
			ID: "embedding-race-batch-newer", ConversationID: conversationID, TurnID: turnID,
			Source: KnowledgeIngestSource{
				ID: "embedding-race-source-newer", Title: "新版本", URL: canonicalURL,
				Snippet: "较新抓取。", Rank: 1, FetchedAtUnixMS: 2,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	olderClaims, err := olderStore.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(olderClaims) != 1 {
		t.Fatalf("older claims=%#v err=%v", olderClaims, err)
	}
	newerClaims, err := newerStore.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(newerClaims) != 1 {
		t.Fatalf("newer claims=%#v err=%v", newerClaims, err)
	}
	document := func(claim KnowledgeIngestClaim, title, content, evidenceID string, fetchedAt int64) KnowledgeDocument {
		t.Helper()
		return KnowledgeDocument{
			SourceID: claim.Task.Source.ID, CanonicalURL: canonicalURL, Title: title,
			Content: content, ContentHash: semanticContentHash(content), EvidenceID: evidenceID,
			ContentType: "text/plain", FetchedAtUnixMS: fetchedAt,
		}
	}
	older := document(
		olderClaims[0],
		"旧版本",
		"较早正文会进入 embedding，但在返回前已有更新版本提交。",
		"web-evidence-embedding-race-older",
		100,
	)
	newer := document(
		newerClaims[0],
		"新版本",
		"较新正文应当在旧任务 embedding 期间成为唯一 current。",
		"web-evidence-embedding-race-newer",
		200,
	)
	for _, item := range []struct {
		store    *Store
		claim    KnowledgeIngestClaim
		document KnowledgeDocument
	}{
		{store: olderStore, claim: olderClaims[0], document: older},
		{store: newerStore, claim: newerClaims[0], document: newer},
	} {
		if needs, err := item.store.KnowledgeDocumentNeedsExtractionContext(
			ctx, item.claim.JobID, item.claim.Task.ID, item.document,
		); err != nil || !needs {
			t.Fatalf("needs extraction=%v err=%v", needs, err)
		}
	}
	oldStatement := "旧任务生成的知识不得越过最终 freshness guard。"
	oldResult := make(chan error, 1)
	go func() {
		_, err := olderStore.CommitKnowledgeDocumentActionsContext(
			ctx,
			olderClaims[0].JobID,
			olderClaims[0].Task.ID,
			older,
			nil,
			[]KnowledgeDocumentAction{{
				Operation:             KnowledgeMutationAdd,
				Content:               oldStatement,
				ConfidenceBasisPoints: 8000,
				Evidence:              older.Content,
			}},
		)
		oldResult <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("older commit did not reach embedding")
	}
	if _, err := newerStore.CommitKnowledgeDocumentActionsContext(
		ctx, newerClaims[0].JobID, newerClaims[0].Task.ID, newer, nil, []KnowledgeDocumentAction{},
	); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-oldResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("older commit did not finish after embedding release")
	}
	var currentHash string
	var versions, knowledge int
	var olderStatus string
	if err := pool.Raw().QueryRow(ctx,
		"SELECT current_content_hash FROM knowledge_documents WHERE canonical_url = $1",
		canonicalURL,
	).Scan(&currentHash); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, `
SELECT count(*)
FROM knowledge_document_versions v
JOIN knowledge_documents d ON d.id = v.document_id
WHERE d.canonical_url = $1`, canonicalURL).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx,
		"SELECT count(*) FROM knowledge_entries WHERE statement = $1",
		oldStatement,
	).Scan(&knowledge); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx,
		"SELECT status FROM knowledge_ingest_jobs WHERE id = $1",
		olderClaims[0].JobID,
	).Scan(&olderStatus); err != nil {
		t.Fatal(err)
	}
	if len(embedder.inputs) != 1 || currentHash != newer.ContentHash ||
		versions != 1 || knowledge != 0 || olderStatus != "succeeded" {
		t.Fatalf(
			"inputs=%#v current=%q versions=%d knowledge=%d status=%q",
			embedder.inputs, currentHash, versions, knowledge, olderStatus,
		)
	}
}

func TestPostgresKnowledgeDocumentUnchangedRefreshBlocksIntermediateStaleCommit(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	currentStore, err := NewStoreFromPoolWithLease(pool, "document-refresh-current", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	staleStore, err := NewStoreFromPoolWithLease(pool, "document-refresh-stale", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, currentStore, "character-document-refresh")
	canonicalURL := "https://public.example/document-refresh"
	enqueue := func(store *Store, batchID, sourceID, snippet string) KnowledgeIngestClaim {
		t.Helper()
		if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{{
			ID: batchID, ConversationID: conversationID, TurnID: turnID,
			Source: KnowledgeIngestSource{
				ID: sourceID, Title: "刷新来源", URL: canonicalURL,
				Snippet: snippet, Rank: 1, FetchedAtUnixMS: 1,
			},
		}}); err != nil {
			t.Fatal(err)
		}
		claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claims=%#v err=%v", claims, err)
		}
		return claims[0]
	}
	document := func(claim KnowledgeIngestClaim, content, evidenceID string, fetchedAt int64) KnowledgeDocument {
		t.Helper()
		return KnowledgeDocument{
			SourceID: claim.Task.Source.ID, CanonicalURL: canonicalURL, Title: "刷新来源",
			Content: content, ContentHash: semanticContentHash(content), EvidenceID: evidenceID,
			ContentType: "text/plain", FetchedAtUnixMS: fetchedAt,
		}
	}

	contentA := "页面当前稳定展示版本 A 的完整正文，后续相同正文刷新应推进观察时间。"
	initialClaim := enqueue(currentStore, "document-refresh-initial", "document-refresh-source-initial", "初始版本。")
	initial := document(initialClaim, contentA, "web-evidence-document-refresh-initial", 100)
	if _, err := currentStore.CommitKnowledgeDocumentActionsContext(
		ctx, initialClaim.JobID, initialClaim.Task.ID, initial, nil, []KnowledgeDocumentAction{},
	); err != nil {
		t.Fatal(err)
	}

	staleClaim := enqueue(staleStore, "document-refresh-stale", "document-refresh-source-stale", "中间版本。")
	stale := document(
		staleClaim,
		"页面在中间时刻短暂展示版本 B，这个慢任务不应越过后续 A 刷新。",
		"web-evidence-document-refresh-stale",
		200,
	)
	if needs, err := staleStore.KnowledgeDocumentNeedsExtractionContext(
		ctx, staleClaim.JobID, staleClaim.Task.ID, stale,
	); err != nil || !needs {
		t.Fatalf("stale needs extraction=%v err=%v", needs, err)
	}

	refreshClaim := enqueue(currentStore, "document-refresh-unchanged", "document-refresh-source-unchanged", "相同正文刷新。")
	refresh := document(
		refreshClaim,
		contentA,
		"web-evidence-document-refresh-unchanged",
		300,
	)
	if needs, err := currentStore.KnowledgeDocumentNeedsExtractionContext(
		ctx, refreshClaim.JobID, refreshClaim.Task.ID, refresh,
	); err != nil || needs {
		t.Fatalf("unchanged refresh needs extraction=%v err=%v", needs, err)
	}

	var currentHash string
	var currentFetchedAt int64
	readCurrent := func() {
		t.Helper()
		if err := pool.Raw().QueryRow(ctx, `
SELECT d.current_content_hash, v.fetched_at_ms
FROM knowledge_documents d
JOIN knowledge_document_versions v ON v.id = d.current_version_id
WHERE d.canonical_url = $1`, canonicalURL).Scan(&currentHash, &currentFetchedAt); err != nil {
			t.Fatal(err)
		}
	}
	readCurrent()
	if currentHash != initial.ContentHash || currentFetchedAt != refresh.FetchedAtUnixMS {
		t.Fatalf(
			"unchanged refresh current hash=%q fetchedAt=%d, want hash=%q fetchedAt=%d",
			currentHash, currentFetchedAt, initial.ContentHash, refresh.FetchedAtUnixMS,
		)
	}

	if _, err := staleStore.CommitKnowledgeDocumentActionsContext(
		ctx, staleClaim.JobID, staleClaim.Task.ID, stale, nil, []KnowledgeDocumentAction{},
	); err != nil {
		t.Fatal(err)
	}
	readCurrent()
	var versions int
	if err := pool.Raw().QueryRow(ctx, `
SELECT count(*)
FROM knowledge_document_versions v
JOIN knowledge_documents d ON d.id = v.document_id
WHERE d.canonical_url = $1`, canonicalURL).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if currentHash != initial.ContentHash || currentFetchedAt != refresh.FetchedAtUnixMS || versions != 1 {
		t.Fatalf(
			"stale commit crossed unchanged refresh: hash=%q fetchedAt=%d versions=%d, want hash=%q fetchedAt=%d versions=1",
			currentHash, currentFetchedAt, versions, initial.ContentHash, refresh.FetchedAtUnixMS,
		)
	}
}

func TestPostgresKnowledgeDocumentEqualFetchTimeKeepsFirstCommitCurrent(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	first, err := NewStoreFromPoolWithLease(pool, "document-equal-first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStoreFromPoolWithLease(pool, "document-equal-second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, first, "character-document-equal")
	canonicalURL := "https://public.example/document-equal"
	if err := first.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{
		{
			ID: "document-equal-batch-a", ConversationID: conversationID, TurnID: turnID,
			Source: KnowledgeIngestSource{
				ID: "document-equal-source-a", Title: "同时抓取", URL: canonicalURL,
				Snippet: "同一毫秒的第一个任务。", Rank: 1, FetchedAtUnixMS: 1,
			},
		},
		{
			ID: "document-equal-batch-b", ConversationID: conversationID, TurnID: turnID,
			Source: KnowledgeIngestSource{
				ID: "document-equal-source-b", Title: "同时抓取", URL: canonicalURL,
				Snippet: "同一毫秒的第二个任务。", Rank: 1, FetchedAtUnixMS: 1,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	firstClaims, err := first.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(firstClaims) != 1 {
		t.Fatalf("first claims=%#v err=%v", firstClaims, err)
	}
	secondClaims, err := second.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(secondClaims) != 1 {
		t.Fatalf("second claims=%#v err=%v", secondClaims, err)
	}
	document := func(claim KnowledgeIngestClaim, content, evidenceID string) KnowledgeDocument {
		t.Helper()
		return KnowledgeDocument{
			SourceID: claim.Task.Source.ID, CanonicalURL: canonicalURL, Title: "同时抓取",
			Content: content, ContentHash: semanticContentHash(content), EvidenceID: evidenceID,
			ContentType: "text/plain", FetchedAtUnixMS: 500,
		}
	}
	firstDocument := document(
		firstClaims[0],
		"同一毫秒内第一个成功提交的完整正文应保持为 current。",
		"web-evidence-document-equal-first",
	)
	secondDocument := document(
		secondClaims[0],
		"同一毫秒内后提交的不同完整正文应作为 stale job 收口。",
		"web-evidence-document-equal-second",
	)
	if _, err := first.CommitKnowledgeDocumentActionsContext(
		ctx, firstClaims[0].JobID, firstClaims[0].Task.ID, firstDocument, nil, []KnowledgeDocumentAction{},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := second.CommitKnowledgeDocumentActionsContext(
		ctx, secondClaims[0].JobID, secondClaims[0].Task.ID, secondDocument, nil, []KnowledgeDocumentAction{},
	); err != nil {
		t.Fatal(err)
	}

	var currentHash string
	var versions int
	if err := pool.Raw().QueryRow(ctx,
		"SELECT current_content_hash FROM knowledge_documents WHERE canonical_url = $1",
		canonicalURL,
	).Scan(&currentHash); err != nil {
		t.Fatal(err)
	}
	if err := pool.Raw().QueryRow(ctx, `
SELECT count(*)
FROM knowledge_document_versions v
JOIN knowledge_documents d ON d.id = v.document_id
WHERE d.canonical_url = $1`, canonicalURL).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if currentHash != firstDocument.ContentHash || versions != 1 {
		t.Fatalf("equal fetch current hash=%q versions=%d, want hash=%q versions=1", currentHash, versions, firstDocument.ContentHash)
	}
}

func TestPostgresKnowledgeIngestRetryBackoffAndManualOperations(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "retry-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, store, "character-retry")
	batch := KnowledgeIngestTask{
		ID: "retry-batch", ConversationID: conversationID, TurnID: turnID,
		Source: KnowledgeIngestSource{
			ID: "retry-source", Title: "来源", URL: "https://public.example/retry",
			Snippet: "重试测试来源。", Rank: 1, FetchedAtUnixMS: 1,
		},
	}
	if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{batch}); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims=%#v err=%v", claims, err)
	}
	before := time.Now().UnixMilli()
	if err := store.RetryClaimedKnowledgeIngestJobContext(ctx, claims[0].JobID, "model_provider", "temporary failure"); err != nil {
		t.Fatal(err)
	}
	records, err := store.KnowledgeIngestJobsContext(ctx, "pending")
	if err != nil || len(records) != 1 {
		t.Fatalf("pending jobs=%#v err=%v", records, err)
	}
	if records[0].AttemptCount != 1 || records[0].NextAttemptAtMS <= before || records[0].ErrorCategory != "model_provider" {
		t.Fatalf("pending retry=%#v", records[0])
	}
	if claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1); err != nil || len(claims) != 0 {
		t.Fatalf("backoff claims=%#v err=%v", claims, err)
	}
	if _, err := pool.Raw().Exec(ctx, "UPDATE knowledge_ingest_jobs SET next_attempt_at_ms = 0 WHERE id = $1", records[0].ID); err != nil {
		t.Fatal(err)
	}
	claims, err = store.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("second claims=%#v err=%v", claims, err)
	}
	if err := store.FailClaimedKnowledgeIngestJobContext(ctx, claims[0].JobID, "invalid model output"); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryKnowledgeIngestJobContext(ctx, claims[0].JobID); err != nil {
		t.Fatal(err)
	}
	if err := store.DropKnowledgeIngestJobContext(ctx, claims[0].JobID); err != nil {
		t.Fatal(err)
	}
	dropped, err := store.KnowledgeIngestJobsContext(ctx, "dropped")
	if err != nil || len(dropped) != 1 || dropped[0].ErrorCategory != "manual_drop" {
		t.Fatalf("dropped jobs=%#v err=%v", dropped, err)
	}
}

func TestPostgresKnowledgeIngestWaitsForCompletedTurnAndDropsFailedTurn(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "turn-gated-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-turn-gated")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.BeginTurn(bootstrap.Conversation.ID, "查询公开资料")
	if err != nil {
		t.Fatal(err)
	}
	batch := KnowledgeIngestTask{
		ID: "turn-gated-batch", ConversationID: bootstrap.Conversation.ID, TurnID: turn.ID,
		Source: KnowledgeIngestSource{
			ID: "turn-gated-source", Title: "来源", URL: "https://public.example/gated",
			Snippet: "等待 Turn 终态。", Rank: 1, FetchedAtUnixMS: 1,
		},
	}
	if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{batch}); err != nil {
		t.Fatal(err)
	}
	if claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1); err != nil || len(claims) != 0 {
		t.Fatalf("interpreting turn claims=%#v err=%v", claims, err)
	}
	if err := store.FailTurn(bootstrap.Conversation.ID, turn.ID, "TEST_FAILURE", "test failure", false); err != nil {
		t.Fatal(err)
	}
	if claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1); err != nil || len(claims) != 0 {
		t.Fatalf("failed turn claims=%#v err=%v", claims, err)
	}
	var status string
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM knowledge_ingest_jobs WHERE task_id = $1", batch.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "dropped" {
		t.Fatalf("failed turn job status=%q", status)
	}
}

func TestPostgresKnowledgeIngestExpiredLeaseReclaimsAndRejectsOldOwner(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	batch := KnowledgeIngestTask{
		ID: "lease-reclaim-batch", ConversationID: bootstrap.Conversation.ID, TurnID: turn.ID,
		Source: KnowledgeIngestSource{
			ID: "lease-reclaim-source", Title: "topic", URL: "https://example.test",
			Snippet: "这是一条足够长的待处理知识摘要。", Rank: 1,
		},
	}
	if err := first.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{batch}); err != nil {
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
	if err := first.RenewKnowledgeIngestLeaseContext(ctx, claimed[0].ID); err != nil {
		t.Fatal(err)
	}
	if blocked, err := ClaimKnowledgeIngestJobs(ctx, pool.Raw(), 1, now+2, "ingest-owner-second", now+2+time.Minute.Milliseconds()); err != nil || len(blocked) != 0 {
		t.Fatalf("renewed second claim = %#v, %v", blocked, err)
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
	if err := first.RenewKnowledgeIngestLeaseContext(ctx, claimed[0].ID); err == nil {
		t.Fatal("old owner lease renewal error = nil")
	}
	staleContent := "过期 owner 不得写入这条长期知识事实。"
	staleDocument := KnowledgeDocument{
		SourceID: batch.Source.ID, CanonicalURL: batch.Source.URL, Title: batch.Source.Title,
		Content: staleContent, ContentHash: semanticContentHash(staleContent),
		EvidenceID: "lease-reclaim-evidence", ContentType: "text/plain",
	}
	if _, err := first.CommitKnowledgeDocumentActionsContext(
		ctx, claimed[0].ID, batch.ID, staleDocument, nil,
		[]KnowledgeDocumentAction{{
			Operation: KnowledgeMutationAdd, Content: staleContent,
			ConfidenceBasisPoints: 8000, Evidence: staleContent,
		}},
	); err == nil {
		t.Fatal("old owner document action commit error = nil")
	}
	var staleKnowledge int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM knowledge_entries WHERE statement = '过期 owner 不得写入这条长期知识事实。'").Scan(&staleKnowledge); err != nil {
		t.Fatal(err)
	}
	if staleKnowledge != 0 {
		t.Fatalf("old owner wrote %d knowledge rows", staleKnowledge)
	}
	if err := FinishKnowledgeIngestJob(ctx, pool.Raw(), claimed[0].ID, "ingest-owner-second", "dropped", "", time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresKnowledgeIngestExpiredFinalAttemptBecomesFailed(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversationContext(ctx, "character-ingest-expired-final-attempt")
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
	if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{{
		ID:             "expired-final-attempt-batch",
		ConversationID: bootstrap.Conversation.ID,
		TurnID:         turn.ID,
		Source: KnowledgeIngestSource{
			ID:      "expired-final-attempt-source",
			Title:   "topic",
			URL:     "https://example.test/expired-final-attempt",
			Snippet: "这是一条用于验证最后一次租约过期终态的公开知识摘要。",
			Rank:    1,
		},
	}}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UnixMilli()
	var jobID string
	for attempt := 1; attempt <= MaxKnowledgeIngestAttempts; attempt++ {
		workerID := fmt.Sprintf("ingest-expired-worker-%d", attempt)
		claimed, err := ClaimKnowledgeIngestJobs(ctx, pool.Raw(), 1, now, workerID, now+time.Minute.Milliseconds())
		if err != nil || len(claimed) != 1 {
			t.Fatalf("attempt %d claim = %#v, %v", attempt, claimed, err)
		}
		if attempt == 1 {
			jobID = claimed[0].ID
		}
		if claimed[0].ID != jobID || claimed[0].AttemptCount != attempt {
			t.Fatalf("attempt %d claim job=%q count=%d", attempt, claimed[0].ID, claimed[0].AttemptCount)
		}
		if _, err := pool.Raw().Exec(ctx,
			"UPDATE knowledge_ingest_jobs SET lease_expires_at_ms = $2 WHERE id = $1",
			jobID, now-1,
		); err != nil {
			t.Fatal(err)
		}
		now++
	}

	var preCoordinationStatus string
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM knowledge_ingest_jobs WHERE id = $1", jobID).Scan(&preCoordinationStatus); err != nil {
		t.Fatal(err)
	}
	if preCoordinationStatus != "running" {
		t.Fatalf("expired final-attempt job status before coordination = %q", preCoordinationStatus)
	}

	claimed, err := ClaimKnowledgeIngestJobs(ctx, pool.Raw(), 1, now, "ingest-expired-worker-final", now+time.Minute.Milliseconds())
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claim after final expired attempt = %#v", claimed)
	}
	var status, category, message string
	var owner *string
	var leaseExpires *int64
	var attempts int
	if err := pool.Raw().QueryRow(ctx, `
SELECT status, attempt_count, lease_owner, lease_expires_at_ms,
       COALESCE(error_category, ''), COALESCE(error_message, '')
FROM knowledge_ingest_jobs
WHERE id = $1`, jobID).Scan(&status, &attempts, &owner, &leaseExpires, &category, &message); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || attempts != MaxKnowledgeIngestAttempts || owner != nil || leaseExpires != nil ||
		category != "attempts_exhausted" || message == "" {
		t.Fatalf(
			"final expired job status=%q attempts=%d owner=%v lease=%v category=%q message=%q",
			status, attempts, owner, leaseExpires, category, message,
		)
	}
}

func TestPostgresKnowledgeIngestReleaseRestoresFinalAttemptAndRejectsWrongOwner(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatal(err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "release-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	intruder, err := NewStoreFromPoolWithLease(pool, "release-intruder", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	conversationID, turnID := seedCompletedTurn(t, ctx, store, "character-ingest-release")
	batch := KnowledgeIngestTask{
		ID: "release-batch", ConversationID: conversationID, TurnID: turnID,
		Source: KnowledgeIngestSource{
			ID: "release-source", Title: "来源", URL: "https://public.example/release",
			Snippet: "计划关闭释放任务时不得消耗最后一次处理机会。", Rank: 1, FetchedAtUnixMS: 1,
		},
	}
	if err := store.EnqueueKnowledgeIngestTasksContext(ctx, []KnowledgeIngestTask{batch}); err != nil {
		t.Fatal(err)
	}

	var jobID string
	for attempt := 1; attempt <= MaxKnowledgeIngestAttempts; attempt++ {
		claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
		if err != nil || len(claims) != 1 {
			t.Fatalf("attempt %d claims=%#v err=%v", attempt, claims, err)
		}
		jobID = claims[0].JobID
		if attempt == MaxKnowledgeIngestAttempts {
			break
		}
		if err := store.RetryClaimedKnowledgeIngestJobContext(ctx, jobID, "model_provider", "temporary failure"); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Raw().Exec(ctx,
			"UPDATE knowledge_ingest_jobs SET next_attempt_at_ms = 0 WHERE id = $1",
			jobID,
		); err != nil {
			t.Fatal(err)
		}
	}

	if err := intruder.ReleaseClaimedKnowledgeIngestJobContext(ctx, jobID); err == nil {
		t.Fatal("wrong owner release error = nil")
	}
	var status string
	var attempts int
	var owner *string
	var lease *int64
	var nextAttempt int64
	if err := pool.Raw().QueryRow(ctx, `
SELECT status, attempt_count, lease_owner, lease_expires_at_ms, next_attempt_at_ms
FROM knowledge_ingest_jobs
WHERE id = $1`, jobID).Scan(&status, &attempts, &owner, &lease, &nextAttempt); err != nil {
		t.Fatal(err)
	}
	if status != "running" || attempts != MaxKnowledgeIngestAttempts || owner == nil || *owner != "release-owner" || lease == nil {
		t.Fatalf("wrong owner changed status=%q attempts=%d owner=%v lease=%v next=%d", status, attempts, owner, lease, nextAttempt)
	}

	if err := store.ReleaseClaimedKnowledgeIngestJobContext(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	owner = nil
	lease = nil
	if err := pool.Raw().QueryRow(ctx, `
SELECT status, attempt_count, lease_owner, lease_expires_at_ms, next_attempt_at_ms
FROM knowledge_ingest_jobs
WHERE id = $1`, jobID).Scan(&status, &attempts, &owner, &lease, &nextAttempt); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != MaxKnowledgeIngestAttempts-1 || owner != nil || lease != nil || nextAttempt != 0 {
		t.Fatalf("release status=%q attempts=%d owner=%v lease=%v next=%d", status, attempts, owner, lease, nextAttempt)
	}
	claims, err := store.ClaimKnowledgeIngestTasksContext(ctx, 1)
	if err != nil || len(claims) != 1 || claims[0].JobID != jobID {
		t.Fatalf("reclaim after release=%#v err=%v", claims, err)
	}
}

func TestPostgresTrigramRetrievalPreservesScopeLimitsAndStableOrder(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
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

func assertPostgresEmbedding(t *testing.T, ctx context.Context, pool *coredb.Pool, table, itemID, content string, wantEnabled bool) {
	t.Helper()
	if table != "personal_memories" && table != "knowledge_entries" {
		t.Fatalf("unsupported embedding table %q", table)
	}
	query := "SELECT embedding_model_id, embedding_content_hash, embedding IS NOT NULL FROM " + table + " WHERE id = $1"
	var modelID, contentHash *string
	var vectorPresent bool
	if err := pool.Raw().QueryRow(ctx, query, itemID).Scan(&modelID, &contentHash, &vectorPresent); err != nil {
		t.Fatalf("query embedding: %v", err)
	}
	if !wantEnabled {
		if modelID != nil || contentHash != nil || vectorPresent {
			t.Fatalf("embedding = (%v, %v, %v), want all NULL", modelID, contentHash, vectorPresent)
		}
		return
	}
	if modelID == nil || *modelID != SemanticEmbeddingModelID || contentHash == nil || *contentHash != semanticContentHash(content) || !vectorPresent {
		t.Fatalf("embedding = (%v, %v, %v), want model=%q hash=%q", modelID, contentHash, vectorPresent, SemanticEmbeddingModelID, semanticContentHash(content))
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
