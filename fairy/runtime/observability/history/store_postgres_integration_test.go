//go:build integration

package history

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	coredb "fairy/runtime/database"
	"fairy/runtime/observability"
)

func TestStoreRestoresSilentTraceByExternalMessageIDAfterRestart(t *testing.T) {
	pool := openHistoryIntegrationPool(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	conversationID := "conversation-history-" + suffix
	silentTraceID := "trace-silent-" + suffix
	failedTraceID := "trace-failed-" + suffix
	silentMessageID := "message-silent-" + suffix
	failedMessageID := "message-failed-" + suffix

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := pool.Raw().Exec(cleanupCtx, `
DELETE FROM observability_records
WHERE kind = 'trace' AND record_key IN ($1, $2)`, silentTraceID, failedTraceID); err != nil {
			t.Errorf("clean observability integration records: %v", err)
		}
	})

	now := time.Now().UnixMilli()
	writer, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, detail := range []observability.MessageTraceDetail{
		{
			TraceID: silentTraceID, MessageID: silentMessageID, Source: "ambient",
			ConversationID: conversationID, Status: "silent",
			StartedAtUnixMS: now, EndedAtUnixMS: now + 2, DurationMS: 2,
		},
		{
			TraceID: failedTraceID, MessageID: failedMessageID, Source: "ambient",
			ConversationID: conversationID, Status: "failed",
			StartedAtUnixMS: now + 3, EndedAtUnixMS: now + 7, DurationMS: 4,
		},
	} {
		if !writer.EnqueueTrace(detail) {
			writer.Close()
			t.Fatalf("enqueue trace %s", detail.TraceID)
		}
	}
	writer.Close()

	reader, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)

	byMessageID, err := reader.TracesByMessageID(t.Context(), silentMessageID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(byMessageID) != 1 {
		t.Fatalf("traces by message id = %#v", byMessageID)
	}
	assertRestoredSilentTrace(t, byMessageID[0], silentTraceID, silentMessageID, conversationID)

	byTraceID, found, err := reader.Trace(t.Context(), silentTraceID)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("trace %s was not restored", silentTraceID)
	}
	assertRestoredSilentTrace(t, byTraceID, silentTraceID, silentMessageID, conversationID)

	failed, found, err := reader.Trace(t.Context(), failedTraceID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || failed.Status != "failed" || failed.MessageID != failedMessageID || failed.ConversationID != conversationID {
		t.Fatalf("later failed trace = %#v, found = %t", failed, found)
	}
}

func assertRestoredSilentTrace(t *testing.T, detail observability.MessageTraceDetail, traceID, messageID, conversationID string) {
	t.Helper()
	if detail.TraceID != traceID || detail.MessageID != messageID || detail.ConversationID != conversationID || detail.Status != "silent" {
		t.Fatalf("restored silent trace = %#v", detail)
	}
}

func openHistoryIntegrationPool(t *testing.T) *coredb.Pool {
	t.Helper()
	databaseURL := os.Getenv("FAIRY_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://fairy:fairy_test_password@127.0.0.1:15432/fairy_test?sslmode=disable"
	}
	pool, err := coredb.Open(t.Context(), coredb.ShortTimeoutConfig(databaseURL))
	if err != nil {
		t.Fatalf("open observability integration database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := coredb.Migrate(t.Context(), pool.Raw()); err != nil {
		t.Fatalf("migrate observability integration database: %v", err)
	}
	return pool
}
