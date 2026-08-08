package history

import (
	"bytes"
	"encoding/json"
	"sync"
	"testing"

	"fairy/runtime/observability"
)

func TestHistoryEnqueueIsBoundedAndPersistsOnlyPublicProjection(t *testing.T) {
	store := &Store{records: make(chan historyRecord, 1)}
	entry := observability.LogEntry{
		Sequence: 1, TimestampUnixMS: 1000, Level: "info", Logger: "test", Message: "safe",
		Fields: []observability.LogField{{Key: "status", Value: "ok"}},
	}
	if !store.EnqueueLog(entry) {
		t.Fatal("first enqueue was rejected")
	}
	if store.EnqueueLog(entry) {
		t.Fatal("full history queue accepted another record")
	}
	if stats := store.Stats(); stats.Queued != 1 || stats.QueueDropped != 1 {
		t.Fatalf("history stats = %#v", stats)
	}
	record := <-store.records
	for _, forbidden := range [][]byte{[]byte("prompt"), []byte("credential"), []byte("principal")} {
		if bytes.Contains(bytes.ToLower(record.payload), forbidden) {
			t.Fatalf("history payload contains %q: %s", forbidden, record.payload)
		}
	}
}

func TestHistoryCloseDrainsEveryAcceptedRecord(t *testing.T) {
	var (
		mu      sync.Mutex
		written []historyRecord
	)
	store := &Store{
		records: make(chan historyRecord, 4),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		limits:  map[string]int{},
		writeRecord: func(record historyRecord) error {
			mu.Lock()
			written = append(written, record)
			mu.Unlock()
			return nil
		},
	}
	if !store.EnqueueLog(observability.LogEntry{Sequence: 1, TimestampUnixMS: 1000, Message: "one"}) ||
		!store.EnqueueLog(observability.LogEntry{Sequence: 2, TimestampUnixMS: 2000, Message: "two"}) {
		t.Fatal("test records were not accepted")
	}
	go store.run()
	store.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(written) != 2 {
		t.Fatalf("normal close persisted %d records, want 2", len(written))
	}
	if store.EnqueueLog(observability.LogEntry{Sequence: 3, TimestampUnixMS: 3000, Message: "late"}) {
		t.Fatal("closed history store accepted a record")
	}
}

func TestHistoryEnqueueTracePersistsExternalMessageID(t *testing.T) {
	store := &Store{records: make(chan historyRecord, 1)}
	detail := observability.MessageTraceDetail{
		TraceID: "trace-1", MessageID: "qq-message-17", Source: "ambient",
		ConversationID: "conversation-1", Status: "silent", StartedAtUnixMS: 1000,
		EndedAtUnixMS: 1001,
	}
	if !store.EnqueueTrace(detail) {
		t.Fatal("trace enqueue was rejected")
	}
	record := <-store.records
	if record.kind != "trace" || record.key != "trace-1" {
		t.Fatalf("history record = %#v", record)
	}
	var persisted observability.MessageTraceDetail
	if err := json.Unmarshal(record.payload, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.TraceID != "trace-1" || persisted.MessageID != "qq-message-17" || persisted.Status != "silent" {
		t.Fatalf("persisted trace = %#v", persisted)
	}
}
