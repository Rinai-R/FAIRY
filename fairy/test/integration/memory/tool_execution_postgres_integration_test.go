//go:build integration

package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	coredb "fairy/runtime/database"
)

func TestPostgresToolExecutionCASRecoveryAndPrivacyMetadata(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
VALUES ('conversation-tool', 'character-tool', 1, 1);
INSERT INTO conversation_turns(id, conversation_id, sequence, status, extraction_state, created_at_ms, updated_at_ms)
VALUES
  ('turn-tool-1', 'conversation-tool', 1, 'planning', 'ineligible', 1, 1),
  ('turn-tool-2', 'conversation-tool', 2, 'planning', 'ineligible', 2, 2),
  ('turn-tool-3', 'conversation-tool', 3, 'planning', 'ineligible', 3, 3),
  ('turn-tool-4', 'conversation-tool', 4, 'planning', 'ineligible', 4, 4);`); err != nil {
		t.Fatalf("seed turns: %v", err)
	}
	store, err := newMemoryIntegrationStores(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool: %v", err)
	}
	now := time.Now().UnixMilli()
	created, err := store.CreateToolExecution(ctx, CreateToolExecutionInput{
		ConversationID: "conversation-tool", TurnID: "turn-tool-1", CallID: "call-tool-1",
		ToolName: ToolNameDesktopObserve, DeadlineAtUnixMS: now + 60_000,
	})
	if err != nil {
		t.Fatalf("CreateToolExecution: %v", err)
	}
	dispatched, changed, err := store.MarkToolExecutionDispatched(ctx, created.ID)
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
	if _, changed, err := store.CompleteToolExecution(ctx, wrong); err != nil || changed {
		t.Fatalf("wrong call completion = (%v, %v)", changed, err)
	}
	completed, changed, err := store.CompleteToolExecution(ctx, complete)
	if err != nil || !changed || completed.Status != ToolExecutionCompleted {
		t.Fatalf("CompleteToolExecution = (%#v, %v, %v)", completed, changed, err)
	}
	if _, changed, err := store.CompleteToolExecution(ctx, complete); err != nil || changed {
		t.Fatalf("duplicate completion = (%v, %v)", changed, err)
	}

	cancelled, err := store.CreateToolExecution(ctx, CreateToolExecutionInput{
		ConversationID: "conversation-tool", TurnID: "turn-tool-2", CallID: "call-tool-2",
		ToolName: ToolNameDesktopObserve, DeadlineAtUnixMS: now + 60_000,
	})
	if err != nil {
		t.Fatalf("CreateToolExecution(cancel): %v", err)
	}
	count, err := store.CancelToolExecutionsForTurn(ctx, cancelled.ConversationID, cancelled.TurnID, "turn_cancelled", "turn was cancelled")
	if err != nil || count != 1 {
		t.Fatalf("CancelToolExecutionsForTurn = (%d, %v)", count, err)
	}
	late := complete
	late.ID, late.TurnID, late.CallID = cancelled.ID, cancelled.TurnID, cancelled.CallID
	if _, changed, err := store.CompleteToolExecution(ctx, late); err != nil || changed {
		t.Fatalf("late completion = (%v, %v)", changed, err)
	}

	expiring, err := store.CreateToolExecution(ctx, CreateToolExecutionInput{
		ConversationID: "conversation-tool", TurnID: "turn-tool-3", CallID: "call-tool-3",
		ToolName: ToolNameDesktopObserve, DeadlineAtUnixMS: now + 1_000,
	})
	if err != nil {
		t.Fatalf("CreateToolExecution(expire): %v", err)
	}
	if count, err := store.ExpireToolExecutions(ctx, now+2_000); err != nil || count != 1 {
		t.Fatalf("ExpireToolExecutions = (%d, %v)", count, err)
	}
	expired, ok, err := store.LoadToolExecution(ctx, expiring.ID)
	if err != nil || !ok || expired.Status != ToolExecutionFailed || expired.ErrorCode == nil || *expired.ErrorCode != "deadline_exceeded" {
		t.Fatalf("expired execution = (%#v, %v, %v)", expired, ok, err)
	}

	pending, err := store.CreateToolExecution(ctx, CreateToolExecutionInput{
		ConversationID: "conversation-tool", TurnID: "turn-tool-4", CallID: "call-tool-4",
		ToolName: ToolNameDesktopObserve, DeadlineAtUnixMS: now + 60_000,
	})
	if err != nil {
		t.Fatalf("CreateToolExecution(pending): %v", err)
	}
	restarted, err := newMemoryIntegrationStores(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool(restart): %v", err)
	}
	settledCount, err := restarted.SettleRecoveredToolExecutions(ctx)
	if err != nil || settledCount != 2 {
		t.Fatalf("SettleRecoveredToolExecutions = (%d, %v), want (2, nil)", settledCount, err)
	}
	recoveredPending, ok, err := restarted.LoadToolExecution(ctx, pending.ID)
	if err != nil || !ok || recoveredPending.Status != ToolExecutionFailed || recoveredPending.ErrorCode == nil || *recoveredPending.ErrorCode != "core_restarted" {
		t.Fatalf("recovered pending execution = (%#v, %v, %v)", recoveredPending, ok, err)
	}
	recoveredCompleted, ok, err := restarted.LoadToolExecution(ctx, created.ID)
	if err != nil || !ok || recoveredCompleted.Status != ToolExecutionCompleted {
		t.Fatalf("recovered completed execution = (%#v, %v, %v)", recoveredCompleted, ok, err)
	}
	var pendingTurnStatus, pendingTurnCode, pendingTurnMessage string
	if err := pool.Raw().QueryRow(ctx, "SELECT status, error_code, error_message FROM conversation_turns WHERE id = $1", pending.TurnID).Scan(&pendingTurnStatus, &pendingTurnCode, &pendingTurnMessage); err != nil {
		t.Fatalf("load recovered pending turn: %v", err)
	}
	if pendingTurnStatus != "failed" || pendingTurnCode != "DESKTOP_CAPTURE_RECOVERY_FAILED" || pendingTurnMessage != "desktop capture was interrupted by Core restart" {
		t.Fatalf("recovered pending turn = (%q, %q, %q)", pendingTurnStatus, pendingTurnCode, pendingTurnMessage)
	}
	var completedTurnStatus, completedTurnCode, completedTurnMessage string
	if err := pool.Raw().QueryRow(ctx, "SELECT status, error_code, error_message FROM conversation_turns WHERE id = $1", completed.TurnID).Scan(&completedTurnStatus, &completedTurnCode, &completedTurnMessage); err != nil {
		t.Fatalf("load recovered completed turn: %v", err)
	}
	if completedTurnStatus != "failed" || completedTurnCode != "DESKTOP_CAPTURE_RECOVERY_FAILED" || completedTurnMessage != "desktop capture evidence was lost during Core restart" {
		t.Fatalf("recovered completed turn = (%q, %q, %q)", completedTurnStatus, completedTurnCode, completedTurnMessage)
	}

	rows, err := pool.Raw().Query(ctx, `
SELECT column_name, data_type
FROM information_schema.columns
WHERE table_schema = current_schema() AND table_name = 'tool_executions'`)
	if err != nil {
		t.Fatalf("query tool columns: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, dataType string
		if err := rows.Scan(&name, &dataType); err != nil {
			t.Fatal(err)
		}
		if dataType == "bytea" || name == "content" || name == "payload" || name == "result_data" {
			t.Fatalf("tool execution persists raw evidence column %s (%s)", name, dataType)
		}
	}
}
