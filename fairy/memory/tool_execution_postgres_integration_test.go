//go:build integration

package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	dbschema "fairy/coredb/schema"
)

func TestPostgresToolExecutionCASRecoveryAndPrivacyMetadata(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := dbschema.Migrate(ctx, pool.Raw()); err != nil {
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
	store, err := NewStoreFromPool(pool)
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
	restarted, err := NewStoreFromPool(pool)
	if err != nil {
		t.Fatalf("NewStoreFromPool(restart): %v", err)
	}
	recovered, err := restarted.ListPendingToolExecutions(ctx, now)
	if err != nil || len(recovered) != 1 || recovered[0].ID != pending.ID {
		t.Fatalf("ListPendingToolExecutions = (%#v, %v)", recovered, err)
	}
	unsettled, err := restarted.ListRecoverableToolExecutions(ctx)
	if err != nil || len(unsettled) != 2 || unsettled[0].ID != created.ID || unsettled[0].Status != ToolExecutionCompleted || unsettled[1].ID != pending.ID || unsettled[1].Status != ToolExecutionPending {
		t.Fatalf("ListRecoverableToolExecutions = (%#v, %v)", unsettled, err)
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
