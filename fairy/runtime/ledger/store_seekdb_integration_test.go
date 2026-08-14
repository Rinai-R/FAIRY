//go:build integration

package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	historyruntime "fairy/context/history/runtime"
	"fairy/runtime/seekdb"
)

func TestRealSeekDBLedgerStorePersistsUsageCacheAndToolCAS(t *testing.T) {
	instance, database, runtimeConfig := openLedgerSeekDB(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeLedgerSeekDB(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB ledger schema: %v", err)
	}
	if _, err := NewSeekDBStore(nil, runtimeConfig.QueryLimit); !errors.Is(err, ErrSeekDBConnectionEmpty) {
		t.Fatalf("NewSeekDBStore(nil) error = %v", err)
	}
	store, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !store.usesSeekDB() {
		t.Fatal("SeekDB ledger store reported a PostgreSQL fallback")
	}
	runtimeStore, err := historyruntime.NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}

	seedLedgerConversation(t, database, "ledger-conversation-a", "character-usage")
	seedLedgerConversation(t, database, "ledger-conversation-b", "character-usage-b")
	seedLedgerTurn(t, database, "ledger-turn-a", "ledger-conversation-a", 1, "completed")
	seedLedgerTurn(t, database, "ledger-turn-b", "ledger-conversation-b", 1, "failed")
	appendLedgerModelUsage(t, runtimeStore, "ledger-conversation-a", "ledger-turn-a", "respond", 1000, 120, uint64Ptr(400))
	appendLedgerModelUsage(t, runtimeStore, "ledger-conversation-a", "ledger-turn-a", "respond", 1500, 80, uint64Ptr(600))
	appendLedgerTerminal(t, runtimeStore, "ledger-conversation-a", "ledger-turn-a", "completed")
	appendLedgerModelUsage(t, runtimeStore, "ledger-conversation-b", "ledger-turn-b", "respond", 200, 30, nil)
	appendLedgerTerminal(t, runtimeStore, "ledger-conversation-b", "ledger-turn-b", "failed")

	report, err := store.AggregateTokenUsageContext(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if report.TurnCount != 2 || len(report.Turns) != 1 || !report.Truncated {
		t.Fatalf("usage report = %#v", report)
	}
	if report.Turns[0].TurnID != "ledger-turn-b" || report.Turns[0].Status != "failed" || report.Turns[0].CharacterID != "character-usage-b" {
		t.Fatalf("latest usage turn = %#v", report.Turns[0])
	}
	overall := findLedgerLane(t, report.Overall, "respond")
	if overall.InputTokens != 2700 || overall.OutputTokens != 230 || overall.CachedInputTokens != 1000 || overall.CachedObservedInputTokens != 2500 || overall.CallCount != 3 {
		t.Fatalf("overall usage = %#v", overall)
	}

	seedLedgerTurn(t, database, "tool-turn-1", "ledger-conversation-a", 2, "planning")
	seedLedgerTurn(t, database, "tool-turn-2", "ledger-conversation-a", 3, "planning")
	seedLedgerTurn(t, database, "tool-turn-3", "ledger-conversation-a", 4, "planning")
	seedLedgerTurn(t, database, "tool-turn-4", "ledger-conversation-a", 5, "planning")
	now := time.Now().UnixMilli()
	created, err := store.CreateToolExecution(t.Context(), CreateToolExecutionInput{
		ConversationID: "ledger-conversation-a", TurnID: "tool-turn-1", CallID: "call-tool-1",
		ToolName: ToolNameDesktopObserve, DeadlineAtUnixMS: now + 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatched, changed, err := store.MarkToolExecutionDispatched(t.Context(), created.ID)
	if err != nil || !changed || dispatched.AttemptCount != 1 || dispatched.LastDispatchedAtUnixMS == nil {
		t.Fatalf("MarkToolExecutionDispatched = (%#v, %v, %v)", dispatched, changed, err)
	}
	complete := CompleteToolExecutionInput{
		ID: created.ID, ConversationID: created.ConversationID, TurnID: created.TurnID, CallID: created.CallID,
		ResultMediaType: "image/png", ResultWidth: 1280, ResultHeight: 720,
		ResultByteCount: 4096, ResultSHA256: strings.Repeat("a", 64),
	}
	wrong := complete
	wrong.CallID = "wrong-call"
	if _, changed, err := store.CompleteToolExecution(t.Context(), wrong); err != nil || changed {
		t.Fatalf("wrong call completion = (%v, %v)", changed, err)
	}
	completed, changed, err := store.CompleteToolExecution(t.Context(), complete)
	if err != nil || !changed || completed.Status != ToolExecutionCompleted {
		t.Fatalf("CompleteToolExecution = (%#v, %v, %v)", completed, changed, err)
	}
	if _, changed, err := store.CompleteToolExecution(t.Context(), complete); err != nil || changed {
		t.Fatalf("duplicate completion = (%v, %v)", changed, err)
	}

	cancelled, err := store.CreateToolExecution(t.Context(), CreateToolExecutionInput{
		ConversationID: "ledger-conversation-a", TurnID: "tool-turn-2", CallID: "call-tool-2",
		ToolName: ToolNameDesktopObserve, DeadlineAtUnixMS: now + 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	count, err := store.CancelToolExecutionsForTurn(t.Context(), cancelled.ConversationID, cancelled.TurnID, "turn_cancelled", "turn was cancelled")
	if err != nil || count != 1 {
		t.Fatalf("CancelToolExecutionsForTurn = (%d, %v)", count, err)
	}

	expiring, err := store.CreateToolExecution(t.Context(), CreateToolExecutionInput{
		ConversationID: "ledger-conversation-a", TurnID: "tool-turn-3", CallID: "call-tool-3",
		ToolName: ToolNameDesktopObserve, DeadlineAtUnixMS: now + 1_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if count, err := store.ExpireToolExecutions(t.Context(), now+2_000); err != nil || count != 1 {
		t.Fatalf("ExpireToolExecutions = (%d, %v)", count, err)
	}
	expired, ok, err := store.LoadToolExecution(t.Context(), expiring.ID)
	if err != nil || !ok || expired.Status != ToolExecutionFailed || expired.ErrorCode == nil || *expired.ErrorCode != "deadline_exceeded" {
		t.Fatalf("expired execution = (%#v, %v, %v)", expired, ok, err)
	}

	pending, err := store.CreateToolExecution(t.Context(), CreateToolExecutionInput{
		ConversationID: "ledger-conversation-a", TurnID: "tool-turn-4", CallID: "call-tool-4",
		ToolName: ToolNameDesktopObserve, DeadlineAtUnixMS: now + 60_000,
	})
	if err != nil {
		t.Fatal(err)
	}

	var rawEvidence int
	if err := database.QueryRowContext(t.Context(), `
SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = 'tool_executions'
  AND (data_type IN ('blob', 'longblob', 'mediumblob') OR column_name IN ('content', 'payload', 'result_data'))`).Scan(&rawEvidence); err != nil {
		t.Fatal(err)
	}
	if rawEvidence != 0 {
		t.Fatalf("tool_executions persists raw evidence columns: %d", rawEvidence)
	}

	closeLedgerSeekDB(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	restarted, err := seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB ledger runtime: %v", err)
	}
	instance = restarted
	closed = false
	restartedStore, err := NewSeekDBStore(restarted.SQL(), runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	restoredReport, err := restartedStore.AggregateTokenUsageContext(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if restoredReport.TurnCount != 2 || restoredReport.Overall[0].CachedInputTokens != 1000 {
		t.Fatalf("restart usage report = %#v", restoredReport)
	}
	settledCount, err := restartedStore.SettleRecoveredToolExecutions(t.Context())
	if err != nil || settledCount != 2 {
		t.Fatalf("SettleRecoveredToolExecutions = (%d, %v), want (2, nil)", settledCount, err)
	}
	recoveredPending, ok, err := restartedStore.LoadToolExecution(t.Context(), pending.ID)
	if err != nil || !ok || recoveredPending.Status != ToolExecutionFailed || recoveredPending.ErrorCode == nil || *recoveredPending.ErrorCode != "core_restarted" {
		t.Fatalf("recovered pending execution = (%#v, %v, %v)", recoveredPending, ok, err)
	}
	recoveredCompleted, ok, err := restartedStore.LoadToolExecution(t.Context(), created.ID)
	if err != nil || !ok || recoveredCompleted.Status != ToolExecutionCompleted {
		t.Fatalf("recovered completed execution = (%#v, %v, %v)", recoveredCompleted, ok, err)
	}
	assertTurnFailed(t, restarted.SQL(), pending.TurnID, "desktop capture was interrupted by Core restart")
	assertTurnFailed(t, restarted.SQL(), completed.TurnID, "desktop capture evidence was lost during Core restart")
}

func appendLedgerModelUsage(t *testing.T, store *historyruntime.Store, conversationID, turnID, lane string, input, output uint64, cached *uint64) {
	t.Helper()
	cachedJSON := `{"status":"missing","tokens":null}`
	if cached != nil {
		cachedJSON = fmt.Sprintf(`{"status":"observed","tokens":%d}`, *cached)
	}
	metadata := fmt.Sprintf(
		`{"streamEventCount":1,"usage":[{"lane":%q,"historyWindow":1,"usage":{"inputTokens":%d,"outputTokens":%d,"cachedInputTokens":%s,"cacheWriteTokens":{"status":"missing","tokens":null}}}]}`,
		lane, input, output, cachedJSON,
	)
	if _, err := store.AppendTurnRuntimeEventContext(t.Context(), historyruntime.TurnRuntimeEventInput{
		ConversationID: conversationID, TurnID: turnID, EventType: "model", MetadataJSON: metadata,
	}); err != nil {
		t.Fatalf("AppendTurnRuntimeEventContext(model): %v", err)
	}
}

func appendLedgerTerminal(t *testing.T, store *historyruntime.Store, conversationID, turnID, status string) {
	t.Helper()
	if _, err := store.AppendTurnRuntimeEventContext(t.Context(), historyruntime.TurnRuntimeEventInput{
		ConversationID: conversationID, TurnID: turnID, EventType: "terminal", State: &status,
		MetadataJSON: fmt.Sprintf(`{"status":%q}`, status),
	}); err != nil {
		t.Fatalf("AppendTurnRuntimeEventContext(terminal): %v", err)
	}
}

func findLedgerLane(t *testing.T, lanes []UsageLaneAggregate, lane string) UsageLaneAggregate {
	t.Helper()
	for _, aggregate := range lanes {
		if aggregate.Lane == lane {
			return aggregate
		}
	}
	t.Fatalf("lane %q not found in %#v", lane, lanes)
	return UsageLaneAggregate{}
}

func assertTurnFailed(t *testing.T, database *sql.DB, turnID, wantMessage string) {
	t.Helper()
	var status, code, message string
	if err := database.QueryRowContext(t.Context(),
		`SELECT status, error_code, error_message FROM conversation_turns WHERE id = ?`, turnID,
	).Scan(&status, &code, &message); err != nil {
		t.Fatalf("load recovered turn %s: %v", turnID, err)
	}
	if status != "failed" || code != "DESKTOP_CAPTURE_RECOVERY_FAILED" || message != wantMessage {
		t.Fatalf("recovered turn %s = (%q, %q, %q)", turnID, status, code, message)
	}
}

func seedLedgerConversation(t *testing.T, database *sql.DB, conversationID, characterID string) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversations(id, character_id, kind, created_at_ms, updated_at_ms)
VALUES (?, ?, 'character', ?, ?)`, conversationID, characterID, int64(1), int64(1)); err != nil {
		t.Fatalf("seed ledger conversation: %v", err)
	}
}

func seedLedgerTurn(t *testing.T, database *sql.DB, turnID, conversationID string, sequence int64, status string) {
	t.Helper()
	var errorCode, errorMessage any
	if status == "failed" {
		errorCode = "SEEDED_FAILED"
		errorMessage = "seeded failed turn"
	}
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversation_turns(
  id, conversation_id, message_id, sequence, status, origin,
  error_code, error_message, error_retryable,
  extraction_state, extraction_claim_id, extraction_lease_owner, extraction_lease_expires_at_ms,
  extraction_attempt_count, extraction_next_attempt_at_ms,
  extraction_error_code, extraction_error_message, created_at_ms, updated_at_ms
) VALUES (?, ?, NULL, ?, ?, 'user', ?, ?, NULL,
          'ineligible', NULL, NULL, NULL, 0, 0, NULL, NULL, ?, ?)`,
		turnID, conversationID, sequence, status, errorCode, errorMessage, int64(1), int64(1),
	); err != nil {
		t.Fatalf("seed ledger turn: %v", err)
	}
}

func uint64Ptr(value uint64) *uint64 { return &value }

func openLedgerSeekDB(t *testing.T) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	binary := os.Getenv(seekdb.EnvBinaryPath)
	if binary == "" {
		t.Skip(seekdb.EnvBinaryPath + " is not set")
	}
	config := seekdb.Config{
		BinaryPath:    binary,
		LibraryDirs:   filepath.SplitList(os.Getenv(seekdb.EnvLibraryPath)),
		DataDir:       filepath.Join(t.TempDir(), "seekdb-ledger"),
		Address:       reserveLedgerLoopbackAddress(t),
		Database:      seekdb.DefaultDatabase,
		User:          seekdb.DefaultUser,
		ConnectLimit:  5 * time.Second,
		StartLimit:    90 * time.Second,
		QueryLimit:    15 * time.Second,
		ShutdownLimit: 20 * time.Second,
		MaxOpenConns:  16,
		MaxIdleConns:  8,
	}
	instance, err := seekdb.Open(t.Context(), config)
	if err != nil {
		t.Fatalf("open real SeekDB ledger runtime: %v", err)
	}
	return instance, instance.SQL(), config
}

func reserveLedgerLoopbackAddress(t *testing.T) string {
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

func closeLedgerSeekDB(t *testing.T, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close real SeekDB ledger runtime: %v", err)
	}
}
