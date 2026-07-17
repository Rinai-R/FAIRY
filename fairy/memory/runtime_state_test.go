package memory

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRuntimeStateSchemaMigratesV3Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fairy.sqlite3")
	db, err := sql.Open(driverName, path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	_, err = db.Exec(
		"CREATE TABLE schema_meta(singleton INTEGER PRIMARY KEY CHECK(singleton = 1), version INTEGER NOT NULL);" +
			"INSERT INTO schema_meta(singleton, version) VALUES (1, 3);" +
			"CREATE TABLE conversations(id TEXT PRIMARY KEY, character_id TEXT NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);" +
			"CREATE TABLE conversation_turns(id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, sequence INTEGER NOT NULL, status TEXT NOT NULL, extraction_state TEXT NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);" +
			"CREATE TABLE personal_memories(id TEXT PRIMARY KEY, kind TEXT NOT NULL, scope_kind TEXT NOT NULL, character_id TEXT, review_status TEXT NOT NULL, content TEXT NOT NULL, status TEXT NOT NULL, confidence_basis_points INTEGER NOT NULL, source_conversation_id TEXT NOT NULL, source_turn_id TEXT NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);" +
			"CREATE TABLE knowledge_entries(id TEXT PRIMARY KEY, topic TEXT NOT NULL, statement TEXT NOT NULL, status TEXT NOT NULL, verification_basis TEXT NOT NULL, confidence_basis_points INTEGER NOT NULL, source_conversation_id TEXT NOT NULL, source_turn_id TEXT NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);" +
			"CREATE TABLE extraction_batches(id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, character_id TEXT NOT NULL, status TEXT NOT NULL, first_turn_sequence INTEGER NOT NULL, last_turn_sequence INTEGER NOT NULL, created_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL);" +
			"INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ('conversation-1', 'character-1', 1, 1);")
	if err != nil {
		t.Fatalf("seed v3 database error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}

	store, err := OpenOrCreate(path)
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	summary, err := store.Summary()
	if err != nil {
		t.Fatalf("Summary() error = %v", err)
	}
	if summary.SchemaVersion != 5 || summary.Conversations != 1 {
		t.Fatalf("summary = %#v, want schema v5 and preserved conversation", summary)
	}

	db, err = sql.Open(driverName, path)
	if err != nil {
		t.Fatalf("sql.Open() migrated error = %v", err)
	}
	defer db.Close()
	for _, table := range []string{"turn_runtime_events", "lane_continuations", "context_windows"} {
		if !runtimeStateTableExists(t, db, table) {
			t.Fatalf("runtime table %q does not exist", table)
		}
	}
	if !runtimeStateColumnExists(t, db, "context_windows", "last_trigger") {
		t.Fatal("context_windows.last_trigger column does not exist")
	}
}

func TestTurnRuntimeEventsRoundTripAndRejectForbiddenMetadata(t *testing.T) {
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
	planning := "planning"
	first, err := store.AppendTurnRuntimeEvent(TurnRuntimeEventInput{
		ConversationID: bootstrap.Conversation.ID,
		TurnID:         turn.ID,
		EventType:      "transition",
		State:          &planning,
		MetadataJSON:   "{\"slots\":[{\"id\":\"character\",\"hash\":\"" + strings.Repeat("a", 64) + "\"}]}",
	})
	if err != nil {
		t.Fatalf("AppendTurnRuntimeEvent() first error = %v", err)
	}
	if first.Sequence != 1 || first.State == nil || *first.State != "planning" {
		t.Fatalf("first event = %#v", first)
	}
	code := "MODEL_RESPONSE_INVALID"
	if _, err := store.AppendTurnRuntimeEvent(TurnRuntimeEventInput{
		ConversationID: bootstrap.Conversation.ID,
		TurnID:         turn.ID,
		EventType:      "terminal",
		Code:           &code,
		MetadataJSON:   "{\"retryable\":false}",
	}); err != nil {
		t.Fatalf("AppendTurnRuntimeEvent() second error = %v", err)
	}

	events, err := store.ListTurnRuntimeEvents(bootstrap.Conversation.ID, turn.ID)
	if err != nil {
		t.Fatalf("ListTurnRuntimeEvents() error = %v", err)
	}
	if len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 2 || events[1].Code == nil || *events[1].Code != code {
		t.Fatalf("events = %#v", events)
	}
	if _, err := store.AppendTurnRuntimeEvent(TurnRuntimeEventInput{
		ConversationID: bootstrap.Conversation.ID,
		TurnID:         turn.ID,
		EventType:      "leak",
		MetadataJSON:   "{\"decision\":{\"stance\":\"warm\"}}",
	}); err == nil {
		t.Fatal("AppendTurnRuntimeEvent() forbidden metadata error = nil, want error")
	}
}

func TestLaneContinuationRoundTripAndClear(t *testing.T) {
	store, conversationID := runtimeStateStoreAndConversation(t)
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	saved, err := store.SaveLaneContinuation(LaneContinuationRecord{
		ConversationID:     conversationID,
		Lane:               PromptLaneRespond,
		PreviousResponseID: "resp_123",
		RequestShapeHash:   hashA,
		InputPrefixHash:    hashB,
		ResponseItemHash:   hashA,
		WindowRevision:     1,
	})
	if err != nil {
		t.Fatalf("SaveLaneContinuation() error = %v", err)
	}
	if saved.UpdatedAtUnixMS == 0 {
		t.Fatalf("saved continuation missing timestamp: %#v", saved)
	}
	loaded, ok, err := store.LoadLaneContinuation(conversationID, PromptLaneRespond)
	if err != nil {
		t.Fatalf("LoadLaneContinuation() error = %v", err)
	}
	if !ok || loaded.PreviousResponseID != "resp_123" || loaded.RequestShapeHash != hashA || loaded.WindowRevision != 1 {
		t.Fatalf("loaded continuation = %#v, ok=%v", loaded, ok)
	}
	if _, err := store.SaveLaneContinuation(LaneContinuationRecord{
		ConversationID:     conversationID,
		Lane:               PromptLaneRespond,
		PreviousResponseID: "resp_456",
		RequestShapeHash:   hashB,
		InputPrefixHash:    hashA,
		ResponseItemHash:   hashB,
		WindowRevision:     2,
	}); err != nil {
		t.Fatalf("SaveLaneContinuation() update error = %v", err)
	}
	loaded, ok, err = store.LoadLaneContinuation(conversationID, PromptLaneRespond)
	if err != nil {
		t.Fatalf("LoadLaneContinuation() after update error = %v", err)
	}
	if !ok || loaded.PreviousResponseID != "resp_456" || loaded.RequestShapeHash != hashB || loaded.WindowRevision != 2 {
		t.Fatalf("updated continuation = %#v, ok=%v", loaded, ok)
	}
	if err := store.ClearLaneContinuation(conversationID, PromptLaneRespond); err != nil {
		t.Fatalf("ClearLaneContinuation() error = %v", err)
	}
	_, ok, err = store.LoadLaneContinuation(conversationID, PromptLaneRespond)
	if err != nil {
		t.Fatalf("LoadLaneContinuation() after clear error = %v", err)
	}
	if ok {
		t.Fatal("LoadLaneContinuation() ok = true after clear, want false")
	}
}

func TestContextWindowRoundTrip(t *testing.T) {
	store, conversationID := runtimeStateStoreAndConversation(t)
	previousWindowID := "window-1"
	observed := uint64(1234)
	estimated := uint64(1400)
	saved, err := store.SaveContextWindow(ContextWindowRecord{
		ConversationID:         conversationID,
		Lane:                   PromptLaneRespond,
		WindowNumber:           2,
		FirstWindowID:          "window-0",
		PreviousWindowID:       &previousWindowID,
		WindowID:               "window-2",
		ObservedPrefillTokens:  &observed,
		EstimatedPrefillTokens: &estimated,
		LastTrigger:            "completed_usage",
		FailureCount:           1,
		PromptWindowRevision:   3,
	})
	if err != nil {
		t.Fatalf("SaveContextWindow() error = %v", err)
	}
	if saved.UpdatedAtUnixMS == 0 {
		t.Fatalf("saved context window missing timestamp: %#v", saved)
	}
	loaded, ok, err := store.LoadContextWindow(conversationID, PromptLaneRespond)
	if err != nil {
		t.Fatalf("LoadContextWindow() error = %v", err)
	}
	if !ok || loaded.WindowNumber != 2 || loaded.WindowID != "window-2" || loaded.PreviousWindowID == nil || *loaded.PreviousWindowID != previousWindowID || loaded.ObservedPrefillTokens == nil || *loaded.ObservedPrefillTokens != observed || loaded.EstimatedPrefillTokens == nil || *loaded.EstimatedPrefillTokens != estimated || loaded.LastTrigger != "completed_usage" || loaded.FailureCount != 1 || loaded.PromptWindowRevision != 3 {
		t.Fatalf("loaded context window = %#v, ok=%v", loaded, ok)
	}
}

func TestRuntimeStateValidationRejectsInvalidLaneAndHash(t *testing.T) {
	store, conversationID := runtimeStateStoreAndConversation(t)
	if _, err := store.SaveLaneContinuation(LaneContinuationRecord{
		ConversationID:     conversationID,
		Lane:               "agent",
		PreviousResponseID: "resp_123",
		RequestShapeHash:   strings.Repeat("a", 64),
		InputPrefixHash:    strings.Repeat("b", 64),
		ResponseItemHash:   strings.Repeat("c", 64),
		WindowRevision:     1,
	}); err == nil {
		t.Fatal("SaveLaneContinuation() invalid lane error = nil, want error")
	}
	if _, err := store.SaveLaneContinuation(LaneContinuationRecord{
		ConversationID:     conversationID,
		Lane:               PromptLaneRespond,
		PreviousResponseID: "resp_123",
		RequestShapeHash:   strings.Repeat("A", 64),
		InputPrefixHash:    strings.Repeat("b", 64),
		ResponseItemHash:   strings.Repeat("c", 64),
		WindowRevision:     1,
	}); err == nil {
		t.Fatal("SaveLaneContinuation() uppercase hash error = nil, want error")
	}
}

func runtimeStateStoreAndConversation(t *testing.T) (*Store, string) {
	t.Helper()
	store, err := OpenOrCreate(filepath.Join(t.TempDir(), "fairy.sqlite3"))
	if err != nil {
		t.Fatalf("OpenOrCreate() error = %v", err)
	}
	bootstrap, err := store.OpenOrCreateCharacterConversation("character-1")
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	return store, bootstrap.Conversation.ID
}

func runtimeStateTableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?1", name).Scan(&count); err != nil {
		t.Fatalf("checking table %q: %v", name, err)
	}
	return count == 1
}

func runtimeStateColumnExists(t *testing.T, db *sql.DB, table string, column string) bool {
	t.Helper()
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("checking columns for %q: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scanning columns for %q: %v", table, err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating columns for %q: %v", table, err)
	}
	return false
}
