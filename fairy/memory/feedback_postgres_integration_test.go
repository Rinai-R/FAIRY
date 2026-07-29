//go:build integration

package memory

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"fairy/coredb"
)

func TestPostgresFeedbackEventLifecycleIntegration(t *testing.T) {
	ctx := context.Background()
	pool := openIsolatedPostgresStore(t, ctx)
	defer pool.Close()
	if err := coredb.Migrate(ctx, pool.Raw()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Raw().Exec(ctx, `
INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms)
VALUES ('feedback-conversation', 'feedback-character', 1, 1);
INSERT INTO conversation_turns(
  id, conversation_id, sequence, status, origin, error_code, error_message,
  extraction_state, created_at_ms, updated_at_ms
) VALUES
  ('feedback-turn-1', 'feedback-conversation', 1, 'completed', 'user', NULL, NULL, 'pending', 1, 1),
  ('feedback-turn-2', 'feedback-conversation', 2, 'completed', 'user', NULL, NULL, 'pending', 2, 2),
  ('feedback-turn-3', 'feedback-conversation', 3, 'failed', 'user', 'MODEL_FAILED', 'failed', 'ineligible', 3, 3)
`); err != nil {
		t.Fatalf("seed feedback turns: %v", err)
	}
	store, err := NewStoreFromPoolWithLease(pool, "feedback-owner", time.Minute)
	if err != nil {
		t.Fatalf("NewStoreFromPoolWithLease: %v", err)
	}
	inputs := []FeedbackEventInput{
		{
			ID: "personal-event-1", Type: FeedbackPersonalMemory,
			ConversationID: "feedback-conversation", TurnID: "feedback-turn-1",
			CharacterID: "feedback-character", Payload: json.RawMessage(`{}`), Status: "pending",
		},
		{
			ID: "personal-event-2", Type: FeedbackPersonalMemory,
			ConversationID: "feedback-conversation", TurnID: "feedback-turn-2",
			CharacterID: "feedback-character", Payload: json.RawMessage(`{}`), Status: "pending",
		},
		{
			ID: "web-event-completed", Type: FeedbackWebKnowledge,
			ConversationID: "feedback-conversation", TurnID: "feedback-turn-1",
			CharacterID: "feedback-character", Payload: json.RawMessage(`{"url":"https://example.com"}`), Status: "waiting_turn",
		},
		{
			ID: "web-event-failed", Type: FeedbackWebKnowledge,
			ConversationID: "feedback-conversation", TurnID: "feedback-turn-3",
			CharacterID: "feedback-character", Payload: json.RawMessage(`{"url":"https://example.net"}`), Status: "waiting_turn",
		},
	}
	if err := store.EnqueueFeedbackEventsContext(ctx, inputs); err != nil {
		t.Fatalf("EnqueueFeedbackEventsContext: %v", err)
	}
	if err := store.EnqueueFeedbackEventsContext(ctx, inputs); err != nil {
		t.Fatalf("idempotent EnqueueFeedbackEventsContext: %v", err)
	}

	personal, err := store.ClaimPersonalFeedbackEventsContext(ctx, "feedback-conversation", 12)
	if err != nil {
		t.Fatalf("ClaimPersonalFeedbackEventsContext: %v", err)
	}
	if len(personal) != 2 || personal[0].ClaimGroupID == "" || personal[0].ClaimGroupID != personal[1].ClaimGroupID {
		t.Fatalf("personal claim = %#v", personal)
	}
	groupID := personal[0].ClaimGroupID
	if err := store.RenewFeedbackEventGroupContext(ctx, groupID); err != nil {
		t.Fatalf("RenewFeedbackEventGroupContext: %v", err)
	}
	other, err := NewStoreFromPoolWithLease(pool, "feedback-other-owner", time.Minute)
	if err != nil {
		t.Fatalf("NewStoreFromPoolWithLease(other): %v", err)
	}
	if err := other.CompleteFeedbackEventGroupContext(ctx, groupID); err == nil {
		t.Fatal("non-owner completion error = nil")
	}
	if claimed, err := store.ClaimPersonalFeedbackEventsContext(ctx, "feedback-conversation", 12); err != nil || len(claimed) != 0 {
		t.Fatalf("second personal claim = %#v, %v", claimed, err)
	}
	if err := store.ReleaseFeedbackEventGroupContext(ctx, groupID); err != nil {
		t.Fatalf("ReleaseFeedbackEventGroupContext: %v", err)
	}
	personal, err = store.ClaimPersonalFeedbackEventsContext(ctx, "feedback-conversation", 12)
	if err != nil || len(personal) != 2 || personal[0].AttemptCount != 1 {
		t.Fatalf("reclaimed personal events = %#v, %v", personal, err)
	}
	if err := store.CompleteFeedbackEventGroupContext(ctx, personal[0].ClaimGroupID); err != nil {
		t.Fatalf("CompleteFeedbackEventGroupContext: %v", err)
	}

	web, err := store.ClaimFeedbackEventsContext(ctx, FeedbackWebKnowledge, 10)
	if err != nil {
		t.Fatalf("ClaimFeedbackEventsContext: %v", err)
	}
	if len(web) != 1 || web[0].ID != "web-event-completed" || web[0].ClaimGroupID != web[0].ID {
		t.Fatalf("web claim = %#v", web)
	}
	if err := store.RetryFeedbackEventGroupContext(ctx, web[0].ClaimGroupID, "provider", "temporary"); err != nil {
		t.Fatalf("RetryFeedbackEventGroupContext: %v", err)
	}
	var pending, dropped string
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM feedback_events WHERE id = 'web-event-completed'").Scan(&pending); err != nil {
		t.Fatalf("read pending event: %v", err)
	}
	if err := pool.Raw().QueryRow(ctx, "SELECT status FROM feedback_events WHERE id = 'web-event-failed'").Scan(&dropped); err != nil {
		t.Fatalf("read dropped event: %v", err)
	}
	if pending != "pending" || dropped != "dropped" {
		t.Fatalf("event statuses = pending:%q dropped:%q", pending, dropped)
	}
}
