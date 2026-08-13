//go:build integration

package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	historyexpr "fairy/context/history/expression"
	"fairy/runtime/seekdb"
)

const seekDBProjectionEvaluationTime = int64(1_800_000_000_000)

const seekDBProjectionSeedBatchSize = 128

func TestRealSeekDBConversationProjectionsAreBoundedIsolatedAndPersistent(t *testing.T) {
	instance, database, runtimeConfig := openTranscriptSeekDBRuntime(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeTranscriptSeekDBRuntime(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB projection schema: %v", err)
	}
	store, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}

	const characterID = "character-projection"
	defaultConversation, err := store.OpenOrCreateCharacterConversationContext(t.Context(), characterID)
	if err != nil {
		t.Fatalf("open default projection conversation: %v", err)
	}
	groupConversation, err := store.OpenOrCreateEndpointConversationContext(
		t.Context(), characterID, transcriptGroupBinding(), transcriptDigest("projection-group"),
	)
	if err != nil {
		t.Fatalf("open group projection conversation: %v", err)
	}
	privateConversation, err := store.OpenOrCreateEndpointConversationContext(
		t.Context(), characterID,
		transcriptPrivateBinding("qq.onebot", transcriptDigest("projection-owner")),
		transcriptDigest("projection-private"),
	)
	if err != nil {
		t.Fatalf("open private projection conversation: %v", err)
	}
	projectedConversation, err := store.OpenOrCreateEndpointConversationContext(
		t.Context(), characterID, transcriptGroupBinding(), transcriptDigest("projection-omissions"),
	)
	if err != nil {
		t.Fatalf("open projected conversation: %v", err)
	}

	defaultID := defaultConversation.Conversation.ID
	groupID := groupConversation.Conversation.ID
	privateID := privateConversation.Conversation.ID
	projectedID := projectedConversation.Conversation.ID
	seedSeekDBLongProjectionConversation(t, database, defaultID, 4_000, true)
	seedSeekDBLongProjectionConversation(t, database, groupID, 8, false)
	seedSeekDBPrivateRecallConversation(t, database, privateID)
	seedSeekDBOmittedPromptConversation(t, database, projectedID)
	seedSeekDBMetadataOnlyConversation(t, database, "metadata-only-projection", "metadata-character")

	t.Run("metadata reads one row without prompt or transcript", func(t *testing.T) {
		record, err := store.LoadConversationRecordContext(t.Context(), "metadata-only-projection")
		if err != nil {
			t.Fatalf("LoadConversationRecordContext(metadata only): %v", err)
		}
		if record != (ConversationRecord{
			ID: "metadata-only-projection", CharacterID: "metadata-character",
			CreatedAtUnixMS: 1, UpdatedAtUnixMS: 2,
		}) {
			t.Fatalf("metadata-only record = %#v", record)
		}
		defaultRecord, err := store.LoadConversationRecordContext(t.Context(), defaultID)
		if err != nil {
			t.Fatalf("LoadConversationRecordContext(default): %v", err)
		}
		if defaultRecord.ID != defaultID || defaultRecord.CharacterID != characterID {
			t.Fatalf("default metadata = %#v", defaultRecord)
		}
		if _, err := store.LoadConversationRecordContext(t.Context(), "missing-projection-conversation"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("missing metadata error = %v, want sql.ErrNoRows", err)
		}
	})

	t.Run("active prompt excludes old history at the query boundary", func(t *testing.T) {
		large, err := store.LoadConversationPromptContext(t.Context(), defaultID)
		if err != nil {
			t.Fatalf("LoadConversationPromptContext(large): %v", err)
		}
		small, err := store.LoadConversationPromptContext(t.Context(), groupID)
		if err != nil {
			t.Fatalf("LoadConversationPromptContext(small): %v", err)
		}
		assertSeekDBActiveProjection(t, large, 4_000, defaultID)
		assertSeekDBActiveProjection(t, small, 8, groupID)
		if seekDBProjectionContentBytes(large.Messages) != seekDBProjectionContentBytes(small.Messages) {
			t.Fatalf("active prompt bytes grew with compacted history: large=%d small=%d",
				seekDBProjectionContentBytes(large.Messages), seekDBProjectionContentBytes(small.Messages))
		}
		full, err := store.LoadConversationContext(t.Context(), defaultID)
		if err != nil {
			t.Fatalf("LoadConversationContext(large): %v", err)
		}
		if len(full.Messages) != 4_007 {
			t.Fatalf("full transcript messages = %d, want 4007", len(full.Messages))
		}
		if len(large.Messages) >= len(full.Messages) {
			t.Fatalf("active prompt materialized full history: active=%d full=%d", len(large.Messages), len(full.Messages))
		}
		if _, err := store.LoadConversationPromptContext(t.Context(), "missing-projection-conversation"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("missing prompt error = %v, want sql.ErrNoRows", err)
		}
	})

	t.Run("active projection preserves correlated expression messages", func(t *testing.T) {
		prompt, err := store.LoadConversationPromptContext(t.Context(), projectedID)
		if err != nil {
			t.Fatalf("LoadConversationPromptContext(projected): %v", err)
		}
		if len(prompt.Messages) != 2 || prompt.Messages[0].Sequence != 4 || prompt.Messages[1].Sequence != 5 {
			t.Fatalf("projected active messages = %#v", prompt.Messages)
		}
		wantTurnID := seekDBProjectionTurnID(projectedID, 4)
		for _, message := range prompt.Messages {
			if message.TurnID != wantTurnID || message.MessageID != "external-projected-turn" {
				t.Fatalf("correlated projected message = %#v", message)
			}
		}
		assistant := prompt.Messages[1]
		if assistant.Role != "assistant" || assistant.Content != "投影后的回复" || len(assistant.Parts) != 1 ||
			assistant.Parts[0].Kind != historyexpr.Utterance || assistant.Parts[0].Text != assistant.Content ||
			assistant.Parts[0].VisualState != "idle" {
			t.Fatalf("projected assistant expression = %#v", assistant)
		}
	})

	t.Run("activity is fixed-shape and time bounded", func(t *testing.T) {
		large, err := store.LoadConversationActivityContext(t.Context(), defaultID, seekDBProjectionEvaluationTime)
		if err != nil {
			t.Fatalf("LoadConversationActivityContext(large): %v", err)
		}
		small, err := store.LoadConversationActivityContext(t.Context(), groupID, seekDBProjectionEvaluationTime)
		if err != nil {
			t.Fatalf("LoadConversationActivityContext(small): %v", err)
		}
		assertSeekDBProjectionActivity(t, large, defaultID)
		assertSeekDBProjectionActivity(t, small, groupID)
		empty, err := store.LoadConversationActivityContext(t.Context(), "metadata-only-projection", seekDBProjectionEvaluationTime)
		if err != nil {
			t.Fatalf("LoadConversationActivityContext(empty): %v", err)
		}
		if empty.AssistantMessages5Minutes != 0 || empty.AssistantMessages30Minutes != 0 ||
			empty.UserMessages30Minutes != 0 || empty.LastAssistantMessageAtUnixMS != nil {
			t.Fatalf("empty activity = %#v", empty)
		}
		old, err := store.LoadConversationActivityContext(t.Context(), privateID, seekDBProjectionEvaluationTime)
		if err != nil {
			t.Fatalf("LoadConversationActivityContext(old latest): %v", err)
		}
		wantOldLatest := seekDBProjectionEvaluationTime - 40*time.Minute.Milliseconds() + 1
		if old.AssistantMessages5Minutes != 0 || old.AssistantMessages30Minutes != 0 ||
			old.UserMessages30Minutes != 0 || old.LastAssistantMessageAtUnixMS == nil ||
			*old.LastAssistantMessageAtUnixMS != wantOldLatest {
			t.Fatalf("old-latest activity = %#v, want latest %d", old, wantOldLatest)
		}
		if _, err := store.LoadConversationActivityContext(t.Context(), defaultID, 0); err == nil {
			t.Fatal("zero activity evaluation time was accepted")
		}

		futureSequence := int64(4_007)
		if _, err := database.ExecContext(t.Context(), `
UPDATE conversation_messages SET created_at_ms = ?
WHERE conversation_id = ? AND sequence = ?`, seekDBProjectionEvaluationTime+1, defaultID, futureSequence); err != nil {
			t.Fatalf("seed future assistant time: %v", err)
		}
		if _, err := store.LoadConversationActivityContext(t.Context(), defaultID, seekDBProjectionEvaluationTime); err == nil {
			t.Fatal("future assistant timestamp was accepted")
		}
		if _, err := database.ExecContext(t.Context(), `
UPDATE conversation_messages SET created_at_ms = ?
WHERE conversation_id = ? AND sequence = ?`, seekDBProjectionEvaluationTime-time.Minute.Milliseconds(), defaultID, futureSequence); err != nil {
			t.Fatalf("restore assistant time: %v", err)
		}
		if _, err := store.LoadConversationActivityContext(t.Context(), "missing-projection-conversation", seekDBProjectionEvaluationTime); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("missing activity error = %v, want sql.ErrNoRows", err)
		}
	})

	t.Run("compacted recall uses scoped Chinese full text", func(t *testing.T) {
		assertSeekDBConversationContentFullTextIndex(t, database)

		semantic, err := store.SearchCompactedTranscript(
			t.Context(), defaultID, 4_000, "夏天 海边", MaxCompactedTranscriptTurns,
		)
		if err != nil {
			t.Fatalf("SearchCompactedTranscript(Chinese full text): %v", err)
		}
		wantSemanticTurn := seekDBProjectionTurnID(defaultID, 3_995)
		if len(semantic.Turns) != 1 || semantic.Turns[0].TurnID != wantSemanticTurn ||
			semantic.Turns[0].Score <= 0 || len(semantic.Turns[0].Messages) != 2 {
			t.Fatalf("Chinese full-text recall = %#v", semantic)
		}
		assertSeekDBRecalledTurn(t, semantic.Turns[0], defaultID, wantSemanticTurn, "external-summer", []uint64{3_995, 3_996})

		exactCases := []struct {
			name              string
			query             string
			sequence          uint64
			externalMessageID string
		}{
			{name: "single Chinese character", query: "鲨", sequence: 3_992, externalMessageID: "external-history-3992"},
			{name: "literal percent underscore slash", query: "%/_", sequence: 3_993, externalMessageID: "external-history-3993"},
			{name: "mixed case ASCII", query: "exactkey42", sequence: 3_994, externalMessageID: "external-history-3994"},
		}
		for _, test := range exactCases {
			t.Run(test.name, func(t *testing.T) {
				recall, err := store.SearchCompactedTranscript(t.Context(), defaultID, 4_000, test.query, 1)
				if err != nil {
					t.Fatalf("SearchCompactedTranscript(%q exact): %v", test.query, err)
				}
				wantTurnID := seekDBProjectionTurnID(defaultID, test.sequence)
				if len(recall.Turns) != 1 || recall.Truncated || recall.Turns[0].TurnID != wantTurnID || recall.Turns[0].Score != 1 {
					t.Fatalf("exact recall for %q = %#v, want one exact-score turn %q", test.query, recall, wantTurnID)
				}
				assertSeekDBRecalledTurn(
					t, recall.Turns[0], defaultID, wantTurnID, test.externalMessageID, []uint64{test.sequence},
				)
			})
		}

		limited, err := store.SearchCompactedTranscript(t.Context(), defaultID, 4_000, "共同暗号", 1)
		if err != nil {
			t.Fatalf("SearchCompactedTranscript(limited): %v", err)
		}
		wantNewestTurn := seekDBProjectionTurnID(defaultID, 3_999)
		if len(limited.Turns) != 1 || !limited.Truncated || limited.Turns[0].TurnID != wantNewestTurn {
			t.Fatalf("limited recall = %#v, want newest turn %q and truncation", limited, wantNewestTurn)
		}
		assertSeekDBRecalledTurn(t, limited.Turns[0], defaultID, wantNewestTurn, "external-code-new", []uint64{3_999, 4_000})
		for _, message := range limited.Turns[0].Messages {
			if message.Sequence > 4_000 || strings.Contains(message.Content, "cutoff 后") || strings.Contains(message.Content, "private") {
				t.Fatalf("out-of-scope recalled message = %#v", message)
			}
		}

		empty, err := store.SearchCompactedTranscript(
			t.Context(), defaultID, 4_000, "zzzxqv987654321nohit", MaxCompactedTranscriptTurns,
		)
		if err != nil {
			t.Fatalf("SearchCompactedTranscript(no hit): %v", err)
		}
		if empty.Turns == nil || len(empty.Turns) != 0 || empty.Truncated {
			t.Fatalf("empty recall = %#v", empty)
		}
		missing, err := store.SearchCompactedTranscript(
			t.Context(), "missing-projection-conversation", 4_000, "共同暗号", MaxCompactedTranscriptTurns,
		)
		if err != nil {
			t.Fatalf("SearchCompactedTranscript(missing conversation): %v", err)
		}
		if missing.Turns == nil || len(missing.Turns) != 0 || missing.Truncated {
			t.Fatalf("missing-conversation recall = %#v, want typed empty", missing)
		}
	})

	t.Run("projection reads honor cancellation and release connections", func(t *testing.T) {
		before := database.Stats().InUse
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		reads := []struct {
			name string
			read func() error
		}{
			{name: "metadata", read: func() error {
				_, readErr := store.LoadConversationRecordContext(canceled, defaultID)
				return readErr
			}},
			{name: "activity", read: func() error {
				_, readErr := store.LoadConversationActivityContext(canceled, defaultID, seekDBProjectionEvaluationTime)
				return readErr
			}},
			{name: "active prompt", read: func() error {
				_, readErr := store.LoadConversationPromptContext(canceled, defaultID)
				return readErr
			}},
			{name: "compacted recall", read: func() error {
				_, readErr := store.SearchCompactedTranscript(canceled, defaultID, 4_000, "共同暗号", 1)
				return readErr
			}},
		}
		for _, read := range reads {
			err := read.read()
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s canceled error = %v, want context.Canceled", read.name, err)
			}
		}
		deadline := time.Now().Add(2 * time.Second)
		for database.Stats().InUse > before && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := database.Stats().InUse; got > before {
			t.Fatalf("canceled projection reads retained %d connections, before=%d", got, before)
		}
	})

	closeTranscriptSeekDBRuntime(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	restarted, err := seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB projection runtime: %v", err)
	}
	instance, database, closed = restarted, restarted.SQL(), false
	restartedStore, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("projection data survives restart", func(t *testing.T) {
		record, err := restartedStore.LoadConversationRecordContext(t.Context(), "metadata-only-projection")
		if err != nil || record.CharacterID != "metadata-character" {
			t.Fatalf("metadata after restart = %#v, %v", record, err)
		}
		prompt, err := restartedStore.LoadConversationPromptContext(t.Context(), defaultID)
		if err != nil {
			t.Fatalf("active prompt after restart: %v", err)
		}
		assertSeekDBActiveProjection(t, prompt, 4_000, defaultID)
		activity, err := restartedStore.LoadConversationActivityContext(t.Context(), defaultID, seekDBProjectionEvaluationTime)
		if err != nil {
			t.Fatalf("activity after restart: %v", err)
		}
		assertSeekDBProjectionActivity(t, activity, defaultID)
		recall, err := restartedStore.SearchCompactedTranscript(t.Context(), defaultID, 4_000, "共同暗号", 1)
		if err != nil {
			t.Fatalf("recall after restart: %v", err)
		}
		wantTurnID := seekDBProjectionTurnID(defaultID, 3_999)
		if len(recall.Turns) != 1 || recall.Turns[0].TurnID != wantTurnID {
			t.Fatalf("recall after restart = %#v", recall)
		}
	})
}

type seekDBProjectionMessageSeed struct {
	sequence       uint64
	role           string
	content        string
	expressionJSON string
	createdAt      int64
}

type seekDBProjectionTurnSeed struct {
	sequence          uint64
	externalMessageID string
	createdAt         int64
	messages          []seekDBProjectionMessageSeed
}

func seedSeekDBLongProjectionConversation(t *testing.T, database *sql.DB, conversationID string, compactedCount uint64, recallFixture bool) {
	t.Helper()
	turns := make([]seekDBProjectionTurnSeed, 0, compactedCount+7)
	for sequence := uint64(1); sequence <= compactedCount; {
		if recallFixture && sequence == compactedCount-5 {
			turns = append(turns,
				seekDBProjectionPair(
					sequence, "external-summer", "我们约好夏天一起去海边", "记得，等天气暖和就出发。",
					seekDBProjectionEvaluationTime-40*time.Minute.Milliseconds(),
				),
				seekDBProjectionPair(
					sequence+2, "external-code-old", "共同暗号是海风", "这是较早的一轮。",
					seekDBProjectionEvaluationTime-39*time.Minute.Milliseconds(),
				),
				seekDBProjectionPair(
					sequence+4, "external-code-new", "共同暗号是海风", "这是较新的一轮。",
					seekDBProjectionEvaluationTime-38*time.Minute.Milliseconds(),
				),
			)
			sequence += 6
			continue
		}
		role := "user"
		if sequence%2 == 0 {
			role = "assistant"
		}
		content := fmt.Sprintf("过期历史-%04d", sequence)
		switch sequence {
		case 3_992:
			content = "单字精确命中：鲨"
		case 3_993:
			content = "字面符号 token%/_needle"
		case 3_994:
			content = "混合 ASCII ExactKey42 命中"
		}
		createdAt := seekDBProjectionEvaluationTime - 31*time.Minute.Milliseconds() - int64(sequence)
		turns = append(turns, seekDBProjectionTurnSeed{
			sequence: sequence, externalMessageID: fmt.Sprintf("external-history-%d", sequence), createdAt: createdAt,
			messages: []seekDBProjectionMessageSeed{{
				sequence: sequence, role: role, content: content, createdAt: createdAt,
			}},
		})
		sequence++
	}
	for offset, active := range seekDBProjectionActiveMessages() {
		sequence := compactedCount + uint64(offset) + 1
		turns = append(turns, seekDBProjectionTurnSeed{
			sequence: sequence, externalMessageID: fmt.Sprintf("external-active-%d", offset+1), createdAt: active.createdAt,
			messages: []seekDBProjectionMessageSeed{{
				sequence: sequence, role: active.role, content: active.content, createdAt: active.createdAt,
			}},
		})
	}
	seedSeekDBProjectionRows(
		t, database, conversationID, compactedCount, "已压缩历史",
		`{"version":1,"omissions":[]}`, turns,
	)
}

func seedSeekDBPrivateRecallConversation(t *testing.T, database *sql.DB, conversationID string) {
	t.Helper()
	createdAt := seekDBProjectionEvaluationTime - 40*time.Minute.Milliseconds()
	seedSeekDBProjectionRows(t, database, conversationID, 2, "私人历史", `{"version":1,"omissions":[]}`,
		[]seekDBProjectionTurnSeed{seekDBProjectionPair(
			1, "external-private", "共同暗号是海风，private conversation", "private response", createdAt,
		)},
	)
}

func seedSeekDBOmittedPromptConversation(t *testing.T, database *sql.DB, conversationID string) {
	t.Helper()
	parts, err := json.Marshal([]historyexpr.Part{{
		Kind: historyexpr.Utterance, Text: "投影后的回复", VisualState: "idle",
	}})
	if err != nil {
		t.Fatal(err)
	}
	turns := []seekDBProjectionTurnSeed{
		{sequence: 1, externalMessageID: "external-compacted", createdAt: 1, messages: []seekDBProjectionMessageSeed{{sequence: 1, role: "user", content: "已压缩", createdAt: 1}}},
		{sequence: 2, externalMessageID: "external-omitted-user", createdAt: 2, messages: []seekDBProjectionMessageSeed{{sequence: 2, role: "user", content: "已写入记忆的用户消息", createdAt: 2}}},
		{sequence: 3, externalMessageID: "external-omitted-assistant", createdAt: 3, messages: []seekDBProjectionMessageSeed{{sequence: 3, role: "assistant", content: "已写入记忆的回复", createdAt: 3}}},
		{
			sequence: 4, externalMessageID: "external-projected-turn", createdAt: 4,
			messages: []seekDBProjectionMessageSeed{
				{sequence: 4, role: "user", content: "仍在活动窗口", createdAt: 4},
				{sequence: 5, role: "assistant", content: "投影后的回复", expressionJSON: string(parts), createdAt: 5},
			},
		},
	}
	seedSeekDBProjectionRows(
		t, database, conversationID, 1, "更早摘要",
		`{"version":1,"omissions":[{"startMessageSequence":2,"endMessageSequence":3,"reason":"memory_committed","memoryId":"memory-projected"}],"recentTailStartSequence":4}`,
		turns,
	)
}

func seedSeekDBMetadataOnlyConversation(t *testing.T, database *sql.DB, conversationID, characterID string) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversations(id, character_id, kind, created_at_ms, updated_at_ms)
VALUES (?, ?, 'character', 1, 2)`, conversationID, characterID); err != nil {
		t.Fatalf("seed metadata-only SeekDB conversation: %v", err)
	}
}

func seedSeekDBProjectionRows(
	t *testing.T,
	database *sql.DB,
	conversationID string,
	cutoff uint64,
	summary string,
	projectionJSON string,
	turns []seekDBProjectionTurnSeed,
) {
	t.Helper()
	for start := 0; start < len(turns); start += seekDBProjectionSeedBatchSize {
		end := min(start+seekDBProjectionSeedBatchSize, len(turns))
		seedSeekDBProjectionRowBatch(t, database, conversationID, turns[start:end])
	}

	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin SeekDB projection metadata fixture: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), `
UPDATE conversations SET updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE id = ?`, seekDBProjectionEvaluationTime, conversationID); err != nil {
		t.Fatalf("touch SeekDB projection conversation: %v", err)
	}
	if _, err := tx.ExecContext(t.Context(), `
UPDATE prompt_windows
SET revision = 2, summary = ?, cutoff_message_sequence = ?,
    projection_revision = 2, projection_state = ?, updated_at_ms = ?
WHERE conversation_id = ?`, summary, cutoff, projectionJSON, seekDBProjectionEvaluationTime, conversationID); err != nil {
		t.Fatalf("seed SeekDB prompt window: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit SeekDB projection metadata fixture: %v", err)
	}
}

func seedSeekDBProjectionRowBatch(
	t *testing.T,
	database *sql.DB,
	conversationID string,
	turns []seekDBProjectionTurnSeed,
) {
	t.Helper()
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin SeekDB projection row batch: %v", err)
	}
	defer tx.Rollback()
	turnStatement, err := tx.PrepareContext(t.Context(), `
INSERT INTO conversation_turns(
    id, conversation_id, message_id, sequence, status, origin,
    error_code, error_message, error_retryable, extraction_state,
    extraction_claim_id, extraction_lease_owner, extraction_lease_expires_at_ms,
    extraction_attempt_count, extraction_next_attempt_at_ms,
    extraction_error_code, extraction_error_message, created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, 'completed', 'user', NULL, NULL, 0, 'ineligible',
          NULL, NULL, NULL, 0, 0, NULL, NULL, ?, ?)`)
	if err != nil {
		t.Fatalf("prepare SeekDB projection turns: %v", err)
	}
	messageStatement, err := tx.PrepareContext(t.Context(), `
INSERT INTO conversation_messages(
    id, conversation_id, turn_id, sequence, role, content, expression_parts, created_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = turnStatement.Close()
		t.Fatalf("prepare SeekDB projection messages: %v", err)
	}
	for _, turn := range turns {
		turnID := seekDBProjectionTurnID(conversationID, turn.sequence)
		var externalMessageID any
		if turn.externalMessageID != "" {
			externalMessageID = turn.externalMessageID
		}
		if _, err := turnStatement.ExecContext(
			t.Context(), turnID, conversationID, externalMessageID, turn.sequence, turn.createdAt, turn.createdAt,
		); err != nil {
			t.Fatalf("seed SeekDB projection turn %d: %v", turn.sequence, err)
		}
		for _, message := range turn.messages {
			expressionJSON := message.expressionJSON
			if expressionJSON == "" {
				expressionJSON = "[]"
			}
			if _, err := messageStatement.ExecContext(
				t.Context(), seekDBProjectionMessageID(conversationID, message.sequence, message.role),
				conversationID, turnID, message.sequence, message.role, message.content, expressionJSON, message.createdAt,
			); err != nil {
				_ = messageStatement.Close()
				_ = turnStatement.Close()
				t.Fatalf("seed SeekDB projection message %d: %v", message.sequence, err)
			}
		}
	}
	if err := messageStatement.Close(); err != nil {
		_ = turnStatement.Close()
		t.Fatalf("close SeekDB projection message statement: %v", err)
	}
	if err := turnStatement.Close(); err != nil {
		t.Fatalf("close SeekDB projection turn statement: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit SeekDB projection row batch: %v", err)
	}
}

func seekDBProjectionPair(sequence uint64, externalMessageID, userContent, assistantContent string, createdAt int64) seekDBProjectionTurnSeed {
	return seekDBProjectionTurnSeed{
		sequence: sequence, externalMessageID: externalMessageID, createdAt: createdAt,
		messages: []seekDBProjectionMessageSeed{
			{sequence: sequence, role: "user", content: userContent, createdAt: createdAt},
			{sequence: sequence + 1, role: "assistant", content: assistantContent, createdAt: createdAt + 1},
		},
	}
}

func seekDBProjectionActiveMessages() []seekDBProjectionMessageSeed {
	return []seekDBProjectionMessageSeed{
		{role: "assistant", content: "active-1-outside-thirty", createdAt: seekDBProjectionEvaluationTime - 30*time.Minute.Milliseconds() - 1},
		{role: "user", content: "active-2-thirty-boundary", createdAt: seekDBProjectionEvaluationTime - 30*time.Minute.Milliseconds()},
		{role: "assistant", content: "active-3-thirty-boundary", createdAt: seekDBProjectionEvaluationTime - 30*time.Minute.Milliseconds()},
		{role: "assistant", content: "active-4-outside-five", createdAt: seekDBProjectionEvaluationTime - 5*time.Minute.Milliseconds() - 1},
		{role: "assistant", content: "active-5-five-boundary", createdAt: seekDBProjectionEvaluationTime - 5*time.Minute.Milliseconds()},
		{role: "user", content: "active-6-user-recent", createdAt: seekDBProjectionEvaluationTime - 10*time.Minute.Milliseconds()},
		{role: "assistant", content: "共同暗号 cutoff 后 active-7-latest", createdAt: seekDBProjectionEvaluationTime - time.Minute.Milliseconds()},
	}
}

func assertSeekDBActiveProjection(t *testing.T, prompt ConversationPromptContext, cutoff uint64, conversationID string) {
	t.Helper()
	if prompt.Conversation.ID != conversationID || prompt.PromptWindow.ConversationID != conversationID ||
		prompt.PromptWindow.CutoffMessageSequence != cutoff || prompt.PromptWindow.Revision != 2 ||
		prompt.PromptWindow.ProjectionRevision != 2 || prompt.PromptWindow.Summary == nil ||
		*prompt.PromptWindow.Summary != "已压缩历史" {
		t.Fatalf("active prompt metadata = %#v / %#v", prompt.Conversation, prompt.PromptWindow)
	}
	want := seekDBProjectionActiveMessages()
	if len(prompt.Messages) != len(want) {
		t.Fatalf("active messages = %d, want %d", len(prompt.Messages), len(want))
	}
	for index, message := range prompt.Messages {
		wantSequence := cutoff + uint64(index) + 1
		if message.ConversationID != conversationID || message.Sequence != wantSequence ||
			message.Role != want[index].role || message.Content != want[index].content ||
			message.CreatedAtUnixMS != want[index].createdAt {
			t.Fatalf("active message %d = %#v, want sequence=%d role=%q content=%q time=%d",
				index, message, wantSequence, want[index].role, want[index].content, want[index].createdAt)
		}
	}
}

func assertSeekDBProjectionActivity(t *testing.T, activity ConversationActivity, conversationID string) {
	t.Helper()
	wantLatest := seekDBProjectionEvaluationTime - time.Minute.Milliseconds()
	if activity.Conversation.ID != conversationID || activity.AssistantMessages5Minutes != 2 ||
		activity.AssistantMessages30Minutes != 4 || activity.UserMessages30Minutes != 2 ||
		activity.LastAssistantMessageAtUnixMS == nil || *activity.LastAssistantMessageAtUnixMS != wantLatest {
		t.Fatalf("conversation activity = %#v, want conversation=%q assistant=2/4 user=2 latest=%d",
			activity, conversationID, wantLatest)
	}
}

func assertSeekDBRecalledTurn(
	t *testing.T,
	turn CompactedTranscriptTurn,
	conversationID, turnID, externalMessageID string,
	sequences []uint64,
) {
	t.Helper()
	if turn.TurnID != turnID || len(turn.Messages) != len(sequences) {
		t.Fatalf("recalled turn = %#v, want turn=%q sequences=%v", turn, turnID, sequences)
	}
	for index, message := range turn.Messages {
		if message.ConversationID != conversationID || message.TurnID != turnID ||
			message.MessageID != externalMessageID || message.Sequence != sequences[index] {
			t.Fatalf("recalled message %d = %#v, want conversation=%q turn=%q external=%q sequence=%d",
				index, message, conversationID, turnID, externalMessageID, sequences[index])
		}
	}
}

func assertSeekDBConversationContentFullTextIndex(t *testing.T, database *sql.DB) {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), "SHOW INDEX FROM conversation_messages")
	if err != nil {
		t.Fatalf("inspect SeekDB transcript full-text index: %v", err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	positions := make(map[string]int, len(columns))
	for index, column := range columns {
		positions[strings.ToLower(column)] = index
	}
	for _, required := range []string{"key_name", "column_name", "index_type", "comment", "visible"} {
		if _, ok := positions[required]; !ok {
			t.Fatalf("SHOW INDEX lacks %s", required)
		}
	}
	values := make([]sql.RawBytes, len(columns))
	destinations := make([]any, len(columns))
	for index := range values {
		destinations[index] = &values[index]
	}
	found := false
	for rows.Next() {
		if err := rows.Scan(destinations...); err != nil {
			t.Fatal(err)
		}
		if string(values[positions["key_name"]]) != "conversation_messages_content_fts_idx" {
			continue
		}
		if string(values[positions["column_name"]]) != "content" ||
			!strings.EqualFold(string(values[positions["index_type"]]), "FULLTEXT") ||
			!strings.EqualFold(string(values[positions["comment"]]), "available") ||
			!strings.EqualFold(string(values[positions["visible"]]), "YES") {
			t.Fatalf("unexpected transcript full-text index metadata: %q", values)
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("conversation_messages.content lacks the revision 4 FULLTEXT index")
	}
}

func seekDBProjectionContentBytes(messages []MessageRecord) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content)
	}
	return total
}

func seekDBProjectionTurnID(conversationID string, sequence uint64) string {
	return fmt.Sprintf("projection-%s-turn-%d", conversationID, sequence)
}

func seekDBProjectionMessageID(conversationID string, sequence uint64, role string) string {
	return fmt.Sprintf("projection-%s-message-%d-%s", conversationID, sequence, role)
}
