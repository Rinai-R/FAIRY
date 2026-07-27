package observability

import (
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestLogLevelFromEnv(t *testing.T) {
	cases := map[string]zapcore.Level{
		"":         zapcore.InfoLevel,
		"debug":    zapcore.DebugLevel,
		"DEBUG":    zapcore.DebugLevel,
		"warn":     zapcore.WarnLevel,
		"warning":  zapcore.WarnLevel,
		"error":    zapcore.ErrorLevel,
		"nonsense": zapcore.InfoLevel,
	}
	for value, want := range cases {
		t.Setenv("FAIRY_LOG_LEVEL", value)
		if got := LogLevelFromEnv(); got != want {
			t.Fatalf("levelFromEnv(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestNewLoggerAndNewSlogLogger(t *testing.T) {
	logger := NewLogger()
	if logger == nil {
		t.Fatal("NewLogger() returned nil")
	}
	slogLogger := NewSlogLogger(logger)
	if slogLogger == nil {
		t.Fatal("NewSlogLogger() returned nil")
	}
	// Should not panic when used.
	slogLogger.Info("logx smoke test", "ok", true)
}
