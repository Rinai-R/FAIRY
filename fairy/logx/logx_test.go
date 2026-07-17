package logx

import (
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestLevelFromEnv(t *testing.T) {
	cases := map[string]zapcore.Level{
		"":        zapcore.InfoLevel,
		"debug":   zapcore.DebugLevel,
		"DEBUG":   zapcore.DebugLevel,
		"warn":    zapcore.WarnLevel,
		"warning": zapcore.WarnLevel,
		"error":   zapcore.ErrorLevel,
		"nonsense": zapcore.InfoLevel,
	}
	for value, want := range cases {
		t.Setenv("FAIRY_LOG_LEVEL", value)
		if got := levelFromEnv(); got != want {
			t.Fatalf("levelFromEnv(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestNewAndNewSlog(t *testing.T) {
	logger := New()
	if logger == nil {
		t.Fatal("New() returned nil")
	}
	slogLogger := NewSlog(logger)
	if slogLogger == nil {
		t.Fatal("NewSlog() returned nil")
	}
	// Should not panic when used.
	slogLogger.Info("logx smoke test", "ok", true)
}
