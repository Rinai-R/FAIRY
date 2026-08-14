//go:build integration

package history

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fairy/runtime/observability"
	"fairy/runtime/seekdb"
)

func TestRealSeekDBHistoryStorePersistsLogsTracesMetricsRetentionAndRestart(t *testing.T) {
	instance, database, runtimeConfig := openHistorySeekDB(t)
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeHistorySeekDB(t, instance, runtimeConfig.ShutdownLimit)
		}
	})
	if err := seekdb.MigrateSchema(t.Context(), database, seekdb.BuiltinMigrations()); err != nil {
		t.Fatalf("migrate SeekDB observability history schema: %v", err)
	}
	if _, err := NewSeekDBStore(nil, runtimeConfig.QueryLimit); !errors.Is(err, ErrHistorySeekDBConnectionEmpty) {
		t.Fatalf("NewSeekDBStore(nil) error = %v", err)
	}
	writer, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	if writer.usesPostgres() || !writer.usesSeekDB() {
		writer.Close()
		t.Fatal("SeekDB history store reported a PostgreSQL fallback")
	}

	now := time.Now().UnixMilli()
	suffix := fmt.Sprintf("%d", now)
	silentTraceID := "trace-silent-" + suffix
	failedTraceID := "trace-failed-" + suffix
	silentMessageID := "message-silent-" + suffix
	lateFailedMessageID := "message-late-failed-" + suffix
	conversationID := "conversation-history-" + suffix

	if !writer.EnqueueLog(observability.LogEntry{
		Sequence: 1, TimestampUnixMS: now, Level: "info", Logger: "core", Message: "safe-one",
	}) || !writer.EnqueueLog(observability.LogEntry{
		Sequence: 2, TimestampUnixMS: now + 1, Level: "info", Logger: "core", Message: "safe-two",
	}) {
		writer.Close()
		t.Fatal("enqueue logs")
	}
	if !writer.EnqueueMetric(observability.MetricHistoryPoint{
		TimestampUnixMS: now, ProcessStartedUnixMS: now - 1000, HTTPScope: "conversation", Goroutines: 8,
	}) {
		writer.Close()
		t.Fatal("enqueue metric")
	}
	if !writer.EnqueueTrace(observability.MessageTraceDetail{
		TraceID: silentTraceID, MessageID: silentMessageID, Source: "ambient",
		ConversationID: conversationID, Status: "silent",
		StartedAtUnixMS: now, EndedAtUnixMS: now + 2, DurationMS: 2,
		Spans: participationHistorySpans(silentTraceID, now),
	}) || !writer.EnqueueTrace(observability.MessageTraceDetail{
		TraceID: failedTraceID, MessageID: "message-failed-" + suffix, Source: "ambient",
		ConversationID: conversationID, Status: "failed",
		StartedAtUnixMS: now + 3, EndedAtUnixMS: now + 7, DurationMS: 4,
	}) {
		writer.Close()
		t.Fatal("enqueue traces")
	}
	writer.Close()

	lateWriter, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !lateWriter.EnqueueTrace(observability.MessageTraceDetail{
		TraceID: silentTraceID, MessageID: lateFailedMessageID, Source: "ambient",
		ConversationID: "late-" + conversationID, Status: "failed",
		StartedAtUnixMS: now + 20, EndedAtUnixMS: now + 40, DurationMS: 20,
	}) || !lateWriter.EnqueueLog(observability.LogEntry{
		Sequence: 2, TimestampUnixMS: now + 5, Level: "warn", Logger: "core", Message: "safe-two-updated",
	}) {
		lateWriter.Close()
		t.Fatal("enqueue late records")
	}
	lateWriter.Close()

	retention, err := NewSeekDBStore(database, runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	retention.limits = map[string]int{"log": 2, "trace": 10, "metric": 10}
	retention.maxAge = time.Hour
	if err := retention.persistRecordSeekDB(historyRecord{
		kind: "log", key: "expired", recordedAtMS: 1,
		payload: []byte(`{"sequence":99,"timestampUnixMs":1,"message":"expired"}`),
	}); err != nil {
		retention.Close()
		t.Fatalf("persist expired log: %v", err)
	}
	if err := retention.cleanup(t.Context()); err != nil {
		retention.Close()
		t.Fatalf("cleanup observability history: %v", err)
	}
	retention.Close()

	closeHistorySeekDB(t, instance, runtimeConfig.ShutdownLimit)
	closed = true
	restarted, err := seekdb.Open(t.Context(), runtimeConfig)
	if err != nil {
		t.Fatalf("restart SeekDB observability history runtime: %v", err)
	}
	instance = restarted
	closed = false
	reader, err := NewSeekDBStore(restarted.SQL(), runtimeConfig.QueryLimit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	if reader.usesPostgres() {
		t.Fatal("restarted SeekDB history store reported a PostgreSQL fallback")
	}

	logs, err := reader.RecentLogs(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 || logs[0].Message != "safe-one" || logs[1].Message != "safe-two-updated" {
		t.Fatalf("restart logs = %#v", logs)
	}
	for _, entry := range logs {
		payload, _ := json.Marshal(entry)
		for _, forbidden := range [][]byte{[]byte("prompt"), []byte("credential"), []byte("principal")} {
			if bytes.Contains(bytes.ToLower(payload), forbidden) {
				t.Fatalf("restored log contains %q: %s", forbidden, payload)
			}
		}
	}

	metrics, err := reader.RecentMetrics(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || metrics[0].Goroutines != 8 || metrics[0].HTTPScope != "conversation" {
		t.Fatalf("restart metrics = %#v", metrics)
	}

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
	if !found || failed.Status != "failed" {
		t.Fatalf("later failed trace = %#v, found = %t", failed, found)
	}

	var expiredCount int
	if err := restarted.SQL().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM observability_records WHERE record_key = 'expired'`).Scan(&expiredCount); err != nil {
		t.Fatal(err)
	}
	if expiredCount != 0 {
		t.Fatalf("expired observability record survived retention: %d", expiredCount)
	}
}

func openHistorySeekDB(t *testing.T) (*seekdb.Runtime, *sql.DB, seekdb.Config) {
	t.Helper()
	binary := os.Getenv(seekdb.EnvBinaryPath)
	if binary == "" {
		t.Skip(seekdb.EnvBinaryPath + " is not set")
	}
	config := seekdb.Config{
		BinaryPath:    binary,
		LibraryDirs:   filepath.SplitList(os.Getenv(seekdb.EnvLibraryPath)),
		DataDir:       filepath.Join(t.TempDir(), "seekdb-history"),
		Address:       reserveHistoryLoopbackAddress(t),
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
		t.Fatalf("open real SeekDB observability history runtime: %v", err)
	}
	return instance, instance.SQL(), config
}

func reserveHistoryLoopbackAddress(t *testing.T) string {
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

func closeHistorySeekDB(t *testing.T, instance *seekdb.Runtime, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := instance.Close(ctx); err != nil {
		t.Errorf("close real SeekDB observability history runtime: %v", err)
	}
}
