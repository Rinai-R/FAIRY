package observability

import (
	"bytes"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestLogCoreCapturesNamedLoggerAndZapFields(t *testing.T) {
	store := NewLogStore(10)
	logger := zap.New(NewLogCore(store, zapcore.DebugLevel)).Named("companion").With(zap.String("apiKey", "sk-test"))
	logger.Warn("provider Authorization: Bearer abc", zap.String("model", "demo"), zap.Error(assertionError("token=secret")))
	entries := store.Query(LogFilter{}).Entries
	if len(entries) != 1 || entries[0].Logger != "companion" || entries[0].Level != "warn" {
		t.Fatalf("entries = %#v", entries)
	}
	encoded := entries[0].Message
	for _, field := range entries[0].Fields {
		encoded += field.Key + field.Value
	}
	for _, secret := range []string{"sk-test", "abc", "secret"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("captured log leaked %q: %s", secret, encoded)
		}
	}
}

func TestConsoleAndLogCoreBothReceiveEntry(t *testing.T) {
	store := NewLogStore(10)
	var sink bytes.Buffer
	console := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&sink), zapcore.InfoLevel)
	logger := zap.New(zapcore.NewTee(console, NewLogCore(store, zapcore.InfoLevel)))
	logger.Info("hello")
	if !strings.Contains(sink.String(), "hello") || len(store.Query(LogFilter{}).Entries) != 1 {
		t.Fatalf("sink=%q entries=%#v", sink.String(), store.Query(LogFilter{}).Entries)
	}
}

type assertionError string

func (e assertionError) Error() string { return string(e) }
