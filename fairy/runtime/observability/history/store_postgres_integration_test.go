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

func TestStoreKeepsFirstTraceTerminalAcrossRestart(t *testing.T) {
	pool := openHistoryIntegrationPool(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	conversationID := "conversation-history-" + suffix
	silentTraceID := "trace-silent-" + suffix
	failedTraceID := "trace-failed-" + suffix
	deliveryTraceID := "trace-delivery-" + suffix
	silentMessageID := "message-silent-" + suffix
	lateFailedMessageID := "message-late-failed-" + suffix
	failedMessageID := "message-failed-" + suffix

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := pool.Raw().Exec(cleanupCtx, `
DELETE FROM observability_records
WHERE kind = 'trace' AND record_key IN ($1, $2, $3)`, silentTraceID, failedTraceID, deliveryTraceID); err != nil {
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
			Spans: participationHistorySpans(silentTraceID, now),
		},
		{
			TraceID: failedTraceID, MessageID: failedMessageID, Source: "ambient",
			ConversationID: conversationID, Status: "failed",
			StartedAtUnixMS: now + 3, EndedAtUnixMS: now + 7, DurationMS: 4,
		},
		{
			TraceID: deliveryTraceID, MessageID: "message-delivery-" + suffix, Source: "ambient",
			ConversationID: conversationID, TurnID: "turn-delivery-" + suffix, Status: "completed",
			StartedAtUnixMS: now + 8, EndedAtUnixMS: now + 12, DurationMS: 4,
			Spans: []observability.TraceSpan{{
				SpanID: deliveryTraceID + "-receipt", ParentSpanID: deliveryTraceID + "-turn",
				Operation: "Surface 回执", Category: "delivery", Status: "completed",
				StartedAtUnixMS: now + 11, EndedAtUnixMS: now + 11,
				Attributes: map[string]string{"beatId": "final-0", "status": "succeeded", "externalMessageId": "45123"},
			}},
		},
	} {
		if !writer.EnqueueTrace(detail) {
			writer.Close()
			t.Fatalf("enqueue trace %s", detail.TraceID)
		}
	}
	writer.Close()

	lateWriter, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	if !lateWriter.EnqueueTrace(observability.MessageTraceDetail{
		TraceID: silentTraceID, MessageID: lateFailedMessageID, Source: "ambient",
		ConversationID: "late-" + conversationID, Status: "failed",
		StartedAtUnixMS: now + 20, EndedAtUnixMS: now + 40, DurationMS: 20,
	}) {
		lateWriter.Close()
		t.Fatalf("enqueue late failed trace %s", silentTraceID)
	}
	lateWriter.Close()

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

	lateByMessageID, err := reader.TracesByMessageID(t.Context(), lateFailedMessageID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lateByMessageID) != 0 {
		t.Fatalf("late failed trace rewrote the first terminal: %#v", lateByMessageID)
	}

	failed, found, err := reader.Trace(t.Context(), failedTraceID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || failed.Status != "failed" || failed.MessageID != failedMessageID || failed.ConversationID != conversationID {
		t.Fatalf("later failed trace = %#v, found = %t", failed, found)
	}

	delivery, found, err := reader.Trace(t.Context(), deliveryTraceID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || delivery.Status != "completed" || len(delivery.Spans) != 1 {
		t.Fatalf("delivery trace = %#v, found = %t", delivery, found)
	}
	receipt := delivery.Spans[0]
	if receipt.Operation != "Surface 回执" || receipt.Category != "delivery" || receipt.Status != "completed" || receipt.Attributes["beatId"] != "final-0" || receipt.Attributes["externalMessageId"] != "45123" {
		t.Fatalf("restored delivery receipt = %#v", receipt)
	}
}

func assertRestoredSilentTrace(t *testing.T, detail observability.MessageTraceDetail, traceID, messageID, conversationID string) {
	t.Helper()
	if detail.TraceID != traceID || detail.MessageID != messageID || detail.ConversationID != conversationID || detail.Status != "silent" {
		t.Fatalf("restored silent trace = %#v", detail)
	}
	if len(detail.Spans) != 5 {
		t.Fatalf("restored participation spans = %#v", detail.Spans)
	}
	participation := detail.Spans[1]
	for index, span := range detail.Spans[2:] {
		if span.ParentSpanID != participation.SpanID {
			t.Fatalf("participation child %d = %#v", index, span)
		}
	}
	if modelSpan := detail.Spans[3]; modelSpan.Operation != "参与模型调用" || modelSpan.Attributes["lane"] != "participate" || modelSpan.Attributes["inputTokens"] != "31" {
		t.Fatalf("restored participation model span = %#v", modelSpan)
	}
}

func participationHistorySpans(traceID string, startedAt int64) []observability.TraceSpan {
	rootID := traceID + "-root"
	participationID := traceID + "-participation"
	return []observability.TraceSpan{
		{SpanID: rootID, Operation: "消息处理", Category: "message", Status: "silent", StartedAtUnixMS: startedAt, EndedAtUnixMS: startedAt + 2, DurationMS: 2, Attributes: map[string]string{"source": "ambient"}},
		{SpanID: participationID, ParentSpanID: rootID, Operation: "参与判断", Category: "participation", Status: "completed", StartedAtUnixMS: startedAt, EndedAtUnixMS: startedAt + 2, DurationMS: 2, Attributes: map[string]string{"action": "silent"}},
		{SpanID: traceID + "-context", ParentSpanID: participationID, Operation: "参与上下文准备", Category: "context", Status: "completed", StartedAtUnixMS: startedAt, EndedAtUnixMS: startedAt + 1, DurationMS: 1, Attributes: map[string]string{"itemCount": "6"}},
		{SpanID: traceID + "-model", ParentSpanID: participationID, Operation: "参与模型调用", Category: "model", Status: "completed", StartedAtUnixMS: startedAt + 1, EndedAtUnixMS: startedAt + 2, DurationMS: 1, Attributes: map[string]string{"attempt": "1", "lane": "participate", "inputTokens": "31"}},
		{SpanID: traceID + "-compile", ParentSpanID: participationID, Operation: "参与结果编译", Category: "compile", Status: "completed", StartedAtUnixMS: startedAt + 2, EndedAtUnixMS: startedAt + 2, Attributes: map[string]string{"attempt": "1", "action": "silent"}},
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
