package history

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestHistoryCloseIsSafeForZeroValueStore(t *testing.T) {
	var store Store
	store.Close()
	store.Close()
}

func TestHistoryEnqueueDoesNotWaitForPersist(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	store := &Store{
		records: make(chan historyRecord, DefaultHistoryQueueCapacity),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		writeRecord: func(historyRecord) error {
			close(started)
			<-release
			return nil
		},
	}
	go store.run()
	enqueued := make(chan bool, 1)
	go func() {
		enqueued <- store.EnqueueLog(observability.LogEntry{
			Sequence: 1, TimestampUnixMS: 1000, Message: "queued",
		})
	}()
	select {
	case ok := <-enqueued:
		if !ok {
			t.Fatal("enqueue was rejected")
		}
	case <-time.After(time.Second):
		t.Fatal("EnqueueLog waited for SeekDB persist")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("persist worker did not start")
	}
	close(release)
	store.Close()
}

func TestHistoryPersistDoesNotRetryFailedRecord(t *testing.T) {
	var attempts atomic.Int64
	store := &Store{
		records: make(chan historyRecord, 4),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		writeRecord: func(historyRecord) error {
			attempts.Add(1)
			return errors.New("seekdb persist unavailable")
		},
	}
	go store.run()
	if !store.EnqueueLog(observability.LogEntry{Sequence: 1, TimestampUnixMS: 1000, Message: "one"}) ||
		!store.EnqueueLog(observability.LogEntry{Sequence: 2, TimestampUnixMS: 2000, Message: "two"}) {
		t.Fatal("enqueue was rejected")
	}
	store.Close()
	if got := attempts.Load(); got != 2 {
		t.Fatalf("persist attempts = %d, want 2", got)
	}
	if stats := store.Stats(); stats.Queued != 2 || stats.WriteFailed != 2 || stats.QueueDropped != 0 {
		t.Fatalf("history stats = %#v", stats)
	}
}

func TestHistoryPersistFailureWindowAndRecovery(t *testing.T) {
	var (
		mu          sync.Mutex
		diagnostics []sinkDiagnostic
		fail        atomic.Bool
	)
	fail.Store(true)
	store := &Store{
		records: make(chan historyRecord, 8),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		writeRecord: func(historyRecord) error {
			if fail.Load() {
				return errors.New("seekdb persist unavailable")
			}
			return nil
		},
	}
	store.SetSinkDiagnostics(func(component string, recovered bool, err error) {
		mu.Lock()
		diagnostics = append(diagnostics, sinkDiagnostic{component: component, recovered: recovered, err: err})
		mu.Unlock()
	})
	go store.run()
	if !store.EnqueueLog(observability.LogEntry{Sequence: 1, TimestampUnixMS: 1000, Message: "fail-one"}) ||
		!store.EnqueueLog(observability.LogEntry{Sequence: 2, TimestampUnixMS: 2000, Message: "fail-two"}) {
		t.Fatal("failed-window enqueue was rejected")
	}
	waitHistoryCondition(t, "persist failures", func() bool { return store.Stats().WriteFailed == 2 })
	fail.Store(false)
	if !store.EnqueueLog(observability.LogEntry{Sequence: 3, TimestampUnixMS: 3000, Message: "recovered"}) ||
		!store.EnqueueLog(observability.LogEntry{Sequence: 4, TimestampUnixMS: 4000, Message: "steady"}) {
		t.Fatal("recovery enqueue was rejected")
	}
	store.Close()
	got := snapshotSinkDiagnostics(&mu, diagnostics)
	if len(got) != 2 || got[0].component != sinkComponentPersist || got[0].recovered || got[0].err == nil {
		t.Fatalf("persist diagnostics = %#v", got)
	}
	if got[1].component != sinkComponentPersist || !got[1].recovered || got[1].err != nil {
		t.Fatalf("persist recovery = %#v", got)
	}
	if stats := store.Stats(); stats.WriteFailed != 2 || stats.Queued != 4 {
		t.Fatalf("history stats = %#v", stats)
	}
}

func TestHistoryCleanupFailureWindowAndRecovery(t *testing.T) {
	var (
		mu          sync.Mutex
		diagnostics []sinkDiagnostic
		fail        atomic.Bool
	)
	fail.Store(true)
	store := &Store{
		records:            make(chan historyRecord, 8),
		stop:               make(chan struct{}),
		done:               make(chan struct{}),
		cleanupAfterWrites: 1,
		writeRecord:        func(historyRecord) error { return nil },
		cleanupRecords: func(context.Context) error {
			if fail.Load() {
				return errors.New("seekdb cleanup unavailable")
			}
			return nil
		},
	}
	store.SetSinkDiagnostics(func(component string, recovered bool, err error) {
		mu.Lock()
		diagnostics = append(diagnostics, sinkDiagnostic{component: component, recovered: recovered, err: err})
		mu.Unlock()
	})
	go store.run()
	if !store.EnqueueLog(observability.LogEntry{Sequence: 1, TimestampUnixMS: 1000, Message: "cleanup-one"}) ||
		!store.EnqueueLog(observability.LogEntry{Sequence: 2, TimestampUnixMS: 2000, Message: "cleanup-two"}) {
		t.Fatal("cleanup-failure enqueue was rejected")
	}
	waitHistoryCondition(t, "cleanup failures", func() bool { return store.Stats().CleanupFailed == 2 })
	fail.Store(false)
	if !store.EnqueueLog(observability.LogEntry{Sequence: 3, TimestampUnixMS: 3000, Message: "cleanup-recovered"}) ||
		!store.EnqueueLog(observability.LogEntry{Sequence: 4, TimestampUnixMS: 4000, Message: "cleanup-steady"}) {
		t.Fatal("cleanup-recovery enqueue was rejected")
	}
	store.Close()
	got := snapshotSinkDiagnostics(&mu, diagnostics)
	if len(got) != 2 || got[0].component != sinkComponentCleanup || got[0].recovered || got[0].err == nil {
		t.Fatalf("cleanup diagnostics = %#v", got)
	}
	if got[1].component != sinkComponentCleanup || !got[1].recovered || got[1].err != nil {
		t.Fatalf("cleanup recovery = %#v", got)
	}
	if stats := store.Stats(); stats.CleanupFailed != 2 || stats.WriteFailed != 0 || stats.Queued != 4 {
		t.Fatalf("history stats = %#v", stats)
	}
}

type sinkDiagnostic struct {
	component string
	recovered bool
	err       error
}

func snapshotSinkDiagnostics(mu *sync.Mutex, diagnostics []sinkDiagnostic) []sinkDiagnostic {
	mu.Lock()
	defer mu.Unlock()
	copied := make([]sinkDiagnostic, len(diagnostics))
	copy(copied, diagnostics)
	return copied
}

func waitHistoryCondition(t *testing.T, name string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", name)
}
