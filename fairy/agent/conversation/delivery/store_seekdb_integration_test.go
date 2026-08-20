//go:build integration

package delivery

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fairy/runtime/seekdb"
	"fairy/transport/session"
)

func TestRealSeekDBExpressionDeliveryStorePersistsAndRejectsDuplicates(t *testing.T) {
	instance, database, runtimeConfig := openDeliverySeekDB(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeDeliverySeekDB(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB expression delivery schema: %v", err)
	}
	if _, err := NewSeekDBStore(nil, runtimeConfig.QueryLimit); !errors.Is(err, ErrSeekDBRequired) {
		t.Fatalf("NewSeekDBStore(nil) error = %v", err)
	}
	store, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	seedDeliveryConversation(t, database, "delivery-conversation", "delivery-character")
	seedDeliveryTurn(t, database, "delivery-turn", "delivery-conversation", 1)

	succeeded := session.ExpressionDeliveryResult{
		ConversationID: "delivery-conversation", TurnID: "delivery-turn", BeatID: "final-0",
		Status: session.ExpressionDeliverySucceeded, ExternalMessageID: "消息-45123",
	}
	if err := store.Record(t.Context(), succeeded); err != nil {
		t.Fatal(err)
	}
	if err := store.Record(t.Context(), succeeded); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate Record() error = %v", err)
	}
	found, err := store.Lookup(t.Context(), succeeded.ConversationID, succeeded.TurnID, succeeded.BeatID)
	if err != nil || found != succeeded {
		t.Fatalf("Lookup() = %#v, %v", found, err)
	}
	if _, err := store.Lookup(t.Context(), succeeded.ConversationID, succeeded.TurnID, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Lookup(missing) error = %v", err)
	}

	failed := session.ExpressionDeliveryResult{
		ConversationID: "delivery-conversation", TurnID: "delivery-turn", BeatID: "final-1",
		Status: session.ExpressionDeliveryFailed, ErrorMessage: "OneBot rejected image",
	}
	if err := store.Record(t.Context(), failed); err != nil {
		t.Fatal(err)
	}
	lookedUpFailed, err := store.Lookup(t.Context(), failed.ConversationID, failed.TurnID, failed.BeatID)
	if err != nil || lookedUpFailed != failed {
		t.Fatalf("Lookup(failed) = %#v, %v", lookedUpFailed, err)
	}

	orphan := session.ExpressionDeliveryResult{
		ConversationID: "missing-conversation", TurnID: "missing-turn", BeatID: "final-0",
		Status: session.ExpressionDeliverySucceeded, ExternalMessageID: "45123",
	}
	if err := store.Record(t.Context(), orphan); err == nil {
		t.Fatal("Record(missing turn) succeeded")
	}

	closeDeliverySeekDB(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	restarted, err := seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB expression delivery runtime: %v", err)
	}
	instance = restarted
	closed = false
	restartedStore, err := NewSeekDBStore(restarted.SQL(), runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restartedStore.Lookup(t.Context(), succeeded.ConversationID, succeeded.TurnID, succeeded.BeatID)
	if err != nil || restored != succeeded {
		t.Fatalf("restart Lookup() = %#v, %v", restored, err)
	}
}

func seedDeliveryConversation(t *testing.T, database *sql.DB, conversationID, characterID string) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversations(id, character_id, kind, created_at_ms, updated_at_ms)
VALUES (?, ?, 'character', ?, ?)`, conversationID, characterID, int64(1), int64(1)); err != nil {
		t.Fatalf("seed delivery conversation: %v", err)
	}
}

func seedDeliveryTurn(t *testing.T, database *sql.DB, turnID, conversationID string, sequence int64) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
INSERT INTO conversation_turns(
  id, conversation_id, message_id, sequence, status, origin,
  error_code, error_message, error_retryable,
  extraction_state, extraction_claim_id, extraction_lease_owner, extraction_lease_expires_at_ms,
  extraction_attempt_count, extraction_next_attempt_at_ms,
  extraction_error_code, extraction_error_message, created_at_ms, updated_at_ms
) VALUES (?, ?, NULL, ?, 'completed', 'user', NULL, NULL, NULL,
          'pending', NULL, NULL, NULL, 0, 0, NULL, NULL, ?, ?)`,
		turnID, conversationID, sequence, int64(1), int64(1),
	); err != nil {
		t.Fatalf("seed delivery turn: %v", err)
	}
}

func openDeliverySeekDB(t *testing.T) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	library := os.Getenv(seekdb.EnvLibrary)
	if library == "" {
		t.Skip(seekdb.EnvLibrary + " is not set")
	}
	config := seekdb.Config{
		LibraryPath:    library,
		DataDir:       filepath.Join(t.TempDir(), "seekdb-delivery"),
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
		t.Fatalf("open real SeekDB expression delivery runtime: %v", err)
	}
	return instance, instance.SQL(), config
}

func reserveDeliveryLoopbackAddress(t *testing.T) string {
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

func closeDeliverySeekDB(t *testing.T, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close real SeekDB expression delivery runtime: %v", err)
	}
}
