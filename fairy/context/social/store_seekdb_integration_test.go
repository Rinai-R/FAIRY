//go:build integration

package social

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"fairy/runtime/seekdb"
	"fairy/runtime/seekdb/seekdbtest"
)

const (
	socialIntegrationCharacterA    = "social-integration-character-a"
	socialIntegrationCharacterB    = "social-integration-character-b"
	socialIntegrationConversationA = "social-integration-conversation-a"
	socialIntegrationConversationB = "social-integration-conversation-b"
	socialIntegrationConversationC = "social-integration-conversation-c"
)

func TestRealSeekDBSocialStoreIsAtomicAndPersistent(t *testing.T) {
	instance, database, runtimeConfig := openSocialSeekDB(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeSocialSeekDB(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB social schema: %v", err)
	}
	seedSocialIntegrationAuthority(t, database)

	if _, err := NewSeekDBStore(nil, runtimeConfig.QueryLimit); !errors.Is(err, ErrSeekDBConnectionEmpty) {
		t.Fatalf("NewSeekDBStore(nil) error = %v", err)
	}
	if _, err := NewSeekDBStore(database, 0); !errors.Is(err, ErrSeekDBQueryLimitInvalid) {
		t.Fatalf("NewSeekDBStore(zero limit) error = %v", err)
	}
	store := newSocialSeekDBStore(t, database, runtimeConfig.QueryLimit)
	if !store.usesSeekDB() {
		t.Fatal("SeekDB social store reported the wrong backend")
	}

	ctx := t.Context()
	empty, err := store.RetrieveSocialMemoryContext(ctx, socialIntegrationCharacterA, socialIntegrationConversationA, "!?")
	if err != nil || !empty.Empty() {
		t.Fatalf("empty-query RetrieveSocialMemoryContext() = %#v, %v", empty, err)
	}

	entries, err := store.StoreSocialMemoryEntries(ctx, SocialMemoryBatchInput{
		CharacterID: socialIntegrationCharacterA, ConversationID: socialIntegrationConversationA,
		Entries: []SocialMemoryEntryInput{
			{Kind: SocialMemoryEpisode, Situation: "群里讨论找实习", Content: "大家认为项目经历要能经得住追问", RecallCue: "找实习、项目经历和面试焦虑", SourceStartUnixMS: 10, SourceEndUnixMS: 20},
			{Kind: SocialMemoryExpression, Situation: "缓和实习焦虑时", Content: "先短句接住情绪，再说一个具体观察", RecallCue: "实习焦虑和安慰", SourceStartUnixMS: 10, SourceEndUnixMS: 20},
		},
	})
	if err != nil || len(entries) != 2 {
		t.Fatalf("StoreSocialMemoryEntries() = %#v, %v", entries, err)
	}
	assertSocialBinaryContentHash(t, database, entries[0].ID)

	repeated, err := store.StoreSocialMemoryEntries(ctx, SocialMemoryBatchInput{
		CharacterID: socialIntegrationCharacterA, ConversationID: socialIntegrationConversationA,
		Entries: []SocialMemoryEntryInput{
			{Kind: SocialMemoryEpisode, Situation: "群里讨论找实习", Content: "大家认为项目经历要能经得住追问", RecallCue: "找实习、项目经历和面试焦虑", SourceStartUnixMS: 5, SourceEndUnixMS: 40},
		},
	})
	if err != nil || len(repeated) != 1 || repeated[0].ID != entries[0].ID {
		t.Fatalf("duplicate StoreSocialMemoryEntries() = %#v, %v", repeated, err)
	}
	if repeated[0].SourceStartUnixMS != 5 || repeated[0].SourceEndUnixMS != 40 {
		t.Fatalf("duplicate upsert did not merge source range: %#v", repeated[0])
	}

	episodeContext, err := store.RetrieveSocialMemoryContext(ctx, socialIntegrationCharacterA, socialIntegrationConversationA, "项目经历")
	if err != nil || len(episodeContext.Entries) == 0 || episodeContext.Entries[0].Kind != SocialMemoryEpisode {
		t.Fatalf("episode RetrieveSocialMemoryContext() = %#v, %v", episodeContext, err)
	}
	expressionContext, err := store.RetrieveSocialMemoryContext(ctx, socialIntegrationCharacterA, socialIntegrationConversationA, "实习焦虑")
	if err != nil || len(expressionContext.Entries) == 0 || expressionContext.Entries[0].Kind != SocialMemoryExpression {
		t.Fatalf("expression RetrieveSocialMemoryContext() = %#v, %v", expressionContext, err)
	}
	naturalContext, err := store.RetrieveSocialMemoryContext(ctx, socialIntegrationCharacterA, socialIntegrationConversationA, "我最近有点实习焦虑")
	if err != nil || len(naturalContext.Entries) == 0 || naturalContext.Entries[0].Kind != SocialMemoryExpression {
		t.Fatalf("natural-query RetrieveSocialMemoryContext() = %#v, %v", naturalContext, err)
	}
	other, err := store.RetrieveSocialMemoryContext(ctx, socialIntegrationCharacterB, socialIntegrationConversationB, "项目经历")
	if err != nil || len(other.Entries) != 0 {
		t.Fatalf("cross-conversation retrieval = %#v, %v", other, err)
	}
	if _, err := store.RetrieveSocialMemoryContext(ctx, socialIntegrationCharacterB, socialIntegrationConversationA, "项目经历"); err == nil {
		t.Fatal("character mismatch retrieval succeeded")
	}

	if _, err := store.StoreSocialMemoryEntries(ctx, SocialMemoryBatchInput{
		CharacterID: socialIntegrationCharacterA, ConversationID: socialIntegrationConversationC,
		Entries: []SocialMemoryEntryInput{{
			Kind: SocialMemoryBehavior, Situation: "另一个群讨论找实习", Content: "先问清楚项目背景再给建议",
			RecallCue: "找实习、项目背景", SourceStartUnixMS: 30, SourceEndUnixMS: 40,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	allCharacter, err := store.RetrieveCharacterSocialMemoryContext(ctx, socialIntegrationCharacterA, "找实习")
	if err != nil || len(allCharacter.Entries) < 2 {
		t.Fatalf("cross-group character retrieval = %#v, %v", allCharacter, err)
	}
	seenConversations := map[string]bool{}
	for _, entry := range allCharacter.Entries {
		if entry.CharacterID != socialIntegrationCharacterA {
			t.Fatalf("character retrieval crossed character: %#v", entry)
		}
		seenConversations[entry.ConversationID] = true
	}
	if !seenConversations[socialIntegrationConversationA] || !seenConversations[socialIntegrationConversationC] {
		t.Fatalf("character retrieval missed a conversation: %#v", allCharacter)
	}

	note, err := store.UpsertSocialPersonNote(ctx, SocialPersonNoteInput{
		CharacterID: socialIntegrationCharacterA, ConversationID: socialIntegrationConversationA,
		SenderID: "sender-1", SenderName: "甲", Note: "常吐槽项目经历但会接话",
	})
	if err != nil || note.Note != "常吐槽项目经历但会接话" {
		t.Fatalf("UpsertSocialPersonNote() = %#v, %v", note, err)
	}
	updatedNote, err := store.UpsertSocialPersonNote(ctx, SocialPersonNoteInput{
		CharacterID: socialIntegrationCharacterA, ConversationID: socialIntegrationConversationA,
		SenderID: "sender-1", SenderName: "甲甲", Note: "改成更短的人设",
	})
	if err != nil || updatedNote.ID != note.ID || updatedNote.SenderName != "甲甲" {
		t.Fatalf("person note upsert did not reuse row: %#v, %v", updatedNote, err)
	}
	listed, err := store.ListSocialPersonNotes(ctx, socialIntegrationCharacterA, socialIntegrationConversationA, []string{"sender-1"})
	if err != nil || len(listed) != 1 || listed[0].ID != note.ID {
		t.Fatalf("ListSocialPersonNotes() = %#v, %v", listed, err)
	}
	personLeak, err := store.RetrieveSocialMemoryContext(ctx, socialIntegrationCharacterA, socialIntegrationConversationA, "项目经历")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range personLeak.Entries {
		if entry.Kind == "person_note" || entry.ID == note.ID {
			t.Fatalf("person note leaked into social retrieval: %#v", personLeak)
		}
	}

	invalidBatch, err := store.StoreSocialMemoryEntries(ctx, SocialMemoryBatchInput{
		CharacterID: socialIntegrationCharacterA, ConversationID: socialIntegrationConversationA,
		Entries: []SocialMemoryEntryInput{
			{Kind: SocialMemoryEpisode, Situation: "有效情境", Content: "有效内容", RecallCue: "有效召回线索", SourceStartUnixMS: 1, SourceEndUnixMS: 2},
			{Kind: "invalid", Situation: "无效情境", Content: "无效内容", RecallCue: "无效召回线索", SourceStartUnixMS: 1, SourceEndUnixMS: 2},
		},
	})
	if err == nil {
		t.Fatalf("invalid batch succeeded: %#v", invalidBatch)
	}

	turnID := seedSocialCompletedTurn(t, database, socialIntegrationConversationA, 10)
	feedbackInput := SocialFeedbackBatchInput{
		CharacterID: socialIntegrationCharacterA, ConversationID: socialIntegrationConversationA, TurnID: turnID,
		ObservedMessageCount: 1, EvaluatorRevision: "social-feedback-v1",
		Evaluations: []SocialFeedbackEvaluation{{
			EntryID: entries[1].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackNegative,
			Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"later-message"},
		}},
	}
	feedback, err := store.RecordSocialFeedbackBatch(ctx, feedbackInput)
	if err != nil || feedback.NoChange || len(feedback.Events) != 1 {
		t.Fatalf("RecordSocialFeedbackBatch() = %#v, %v", feedback, err)
	}
	if replay, err := store.RecordSocialFeedbackBatch(ctx, feedbackInput); err != nil || !replay.NoChange || replay.Events[0].ID != feedback.Events[0].ID {
		t.Fatalf("idempotent RecordSocialFeedbackBatch() = %#v, %v", replay, err)
	}
	conflicting := feedbackInput
	conflicting.Evaluations = []SocialFeedbackEvaluation{{
		EntryID: entries[1].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackPositive,
		Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"later-message"},
	}}
	if _, err := store.RecordSocialFeedbackBatch(ctx, conflicting); err == nil {
		t.Fatal("conflicting social feedback batch succeeded")
	}

	behavior, err := store.StoreSocialMemoryEntries(ctx, SocialMemoryBatchInput{
		CharacterID: socialIntegrationCharacterA, ConversationID: socialIntegrationConversationA,
		Entries: []SocialMemoryEntryInput{{
			Kind: SocialMemoryBehavior, Situation: "被点名时", Content: "先短回再补一句", RecallCue: "被点名短回",
			SourceStartUnixMS: 10, SourceEndUnixMS: 20,
		}},
	})
	if err != nil || len(behavior) != 1 {
		t.Fatalf("behavior StoreSocialMemoryEntries() = %#v, %v", behavior, err)
	}
	for index := 0; index < SocialNegativeSuppressThreshold; index++ {
		negativeTurn := seedSocialCompletedTurn(t, database, socialIntegrationConversationA, int64(20+index))
		if _, err := store.RecordSocialFeedbackBatch(ctx, SocialFeedbackBatchInput{
			CharacterID: socialIntegrationCharacterA, ConversationID: socialIntegrationConversationA, TurnID: negativeTurn,
			ObservedMessageCount: 1, EvaluatorRevision: "social-feedback-v2",
			Evaluations: []SocialFeedbackEvaluation{{
				EntryID: behavior[0].ID, Adoption: SocialFeedbackAdopted, Outcome: SocialFeedbackNegative,
				Credit: SocialFeedbackCreditEntry, EvidenceMessageIDs: []string{"later-negative"},
			}},
		}); err != nil {
			t.Fatalf("negative feedback %d: %v", index+1, err)
		}
	}
	var status string
	var negativeCount int64
	var quarantinedUntil sql.NullInt64
	if err := database.QueryRowContext(ctx, `
SELECT status, feedback_negative_count, feedback_quarantined_until_ms
FROM social_memory_entries WHERE id = ?`, behavior[0].ID).Scan(&status, &negativeCount, &quarantinedUntil); err != nil {
		t.Fatal(err)
	}
	if status != "suppressed" || negativeCount != int64(SocialNegativeSuppressThreshold) || !quarantinedUntil.Valid {
		t.Fatalf("suppressed aggregate = status:%s neg:%d quarantine:%v", status, negativeCount, quarantinedUntil)
	}
	hidden, err := store.RetrieveSocialMemoryContext(ctx, socialIntegrationCharacterA, socialIntegrationConversationA, "被点名")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range hidden.Entries {
		if entry.ID == behavior[0].ID {
			t.Fatalf("suppressed entry still retrieved: %#v", hidden)
		}
	}

	wildcardEntries, err := store.StoreSocialMemoryEntries(ctx, SocialMemoryBatchInput{
		CharacterID: socialIntegrationCharacterA, ConversationID: socialIntegrationConversationA,
		Entries: []SocialMemoryEntryInput{
			{Kind: SocialMemoryEpisode, Situation: "字面通配", Content: "token qx%_z9abc stays literal", RecallCue: "通配字面", SourceStartUnixMS: 50, SourceEndUnixMS: 60},
			{Kind: SocialMemoryEpisode, Situation: "字面诱饵", Content: "token qxAz9abc must not match", RecallCue: "通配诱饵", SourceStartUnixMS: 50, SourceEndUnixMS: 60},
		},
	})
	if err != nil || len(wildcardEntries) != 2 {
		t.Fatalf("wildcard StoreSocialMemoryEntries() = %#v, %v", wildcardEntries, err)
	}
	literal, err := store.RetrieveSocialMemoryContext(ctx, socialIntegrationCharacterA, socialIntegrationConversationA, "qx%_z9abc")
	if err != nil || len(literal.Entries) != 1 || literal.Entries[0].ID != wildcardEntries[0].ID {
		t.Fatalf("literal-only retrieval = %#v, %v", literal, err)
	}

	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("verify idempotent schema before social restart: %v", err)
	}
	closeSocialSeekDB(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	var reopenErr error
	instance, reopenErr = seekdb.Open(t.Context(), runtimeConfig)
	if reopenErr != nil {
		t.Fatalf("restart SeekDB social runtime: %v", reopenErr)
	}
	closed = false
	database = instance.SQL()
	restarted := newSocialSeekDBStore(t, database, runtimeConfig.QueryLimit)
	restartedContext, err := restarted.RetrieveSocialMemoryContext(t.Context(), socialIntegrationCharacterA, socialIntegrationConversationA, "实习焦虑")
	if err != nil || len(restartedContext.Entries) == 0 || restartedContext.Entries[0].ID != entries[1].ID {
		t.Fatalf("restarted social retrieval = %#v, %v", restartedContext, err)
	}
	restartedNotes, err := restarted.ListSocialPersonNotes(t.Context(), socialIntegrationCharacterA, socialIntegrationConversationA, []string{"sender-1"})
	if err != nil || len(restartedNotes) != 1 || restartedNotes[0].ID != note.ID {
		t.Fatalf("restarted person notes = %#v, %v", restartedNotes, err)
	}
	var eventCount int
	if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM social_memory_feedback_events").Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount == 0 {
		t.Fatal("restarted social feedback events were not persisted")
	}
}

func assertSocialBinaryContentHash(t *testing.T, database *sql.DB, id string) {
	t.Helper()
	var length int
	if err := database.QueryRowContext(t.Context(), `
SELECT OCTET_LENGTH(content_hash) FROM social_memory_entries WHERE id = ?`, id).Scan(&length); err != nil {
		t.Fatalf("read social content hash length: %v", err)
	}
	if length != 32 {
		t.Fatalf("social content hash length = %d, want 32", length)
	}
}

func seedSocialIntegrationAuthority(t *testing.T, database *sql.DB) {
	t.Helper()
	createdAt := int64(1_786_200_000_000)
	for _, fixture := range []struct {
		conversationID, characterID string
	}{
		{socialIntegrationConversationA, socialIntegrationCharacterA},
		{socialIntegrationConversationB, socialIntegrationCharacterB},
		{socialIntegrationConversationC, socialIntegrationCharacterA},
	} {
		if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversations(id, character_id, kind, created_at_ms, updated_at_ms)
VALUES (?, ?, 'character', ?, ?)`,
			fixture.conversationID, fixture.characterID, createdAt, createdAt,
		); err != nil {
			t.Fatalf("seed social conversation %s: %v", fixture.conversationID, err)
		}
	}
}

func seedSocialCompletedTurn(t *testing.T, database *sql.DB, conversationID string, sequence int64) string {
	t.Helper()
	turnID := newID()
	createdAt := int64(1_786_200_000_000) + sequence
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversation_turns(
  id, conversation_id, message_id, sequence, status, origin,
  error_code, error_message, error_retryable,
  extraction_state, extraction_claim_id, extraction_lease_owner, extraction_lease_expires_at_ms,
  extraction_attempt_count, extraction_next_attempt_at_ms,
  extraction_error_code, extraction_error_message, created_at_ms, updated_at_ms
) VALUES (?, ?, NULL, ?, 'completed', 'user', NULL, NULL, NULL,
          'pending', NULL, NULL, NULL, 0, 0, NULL, NULL, ?, ?)`,
		turnID, conversationID, sequence, createdAt, createdAt,
	); err != nil {
		t.Fatalf("seed social turn: %v", err)
	}
	return turnID
}

func newSocialSeekDBStore(t *testing.T, database *sql.DB, queryLimit time.Duration) *Store {
	t.Helper()
	store, err := NewSeekDBStore(database, queryLimit)
	if err != nil {
		t.Fatalf("new SeekDB social store: %v", err)
	}
	return store
}

func openSocialSeekDB(t *testing.T) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	library := os.Getenv(seekdb.EnvLibrary)
	if library == "" {
		t.Skip(seekdb.EnvLibrary + " is not set")
	}
	config := seekdb.Config{
		LibraryPath:   library,
		DataDir:       seekdbtest.DataDir(t),
		Database:      seekdb.DefaultDatabase,
		ConnectLimit:  5 * time.Second,
		StartLimit:    90 * time.Second,
		QueryLimit:    15 * time.Second,
		ShutdownLimit: 20 * time.Second,
		MaxOpenConns:  16,
		MaxIdleConns:  8,
	}
	instance, err := seekdb.Open(t.Context(), config)
	if err != nil {
		t.Fatalf("open real SeekDB social runtime: %v", err)
	}
	return instance, instance.SQL(), config
}

func reserveSocialLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func closeSocialSeekDB(t *testing.T, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close real SeekDB social runtime: %v", err)
	}
}
