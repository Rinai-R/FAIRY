package observability

import (
	"log/slog"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/exp/zapslog"
	"go.uber.org/zap/zapcore"
)

// New builds the application's zap logger, writing human-readable console output
// to stderr. The level defaults to info and may be overridden by FAIRY_LOG_LEVEL
// (debug/info/warn/error).
func NewLogger(extraCores ...zapcore.Core) *zap.Logger {
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		MessageKey:     "msg",
		CallerKey:      zapcore.OmitKey,
		StacktraceKey:  zapcore.OmitKey,
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.TimeEncoderOfLayout(time.Kitchen),
		EncodeDuration: zapcore.StringDurationEncoder,
	}
	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderConfig),
		zapcore.Lock(os.Stderr),
		LogLevelFromEnv(),
	)
	cores := append([]zapcore.Core{core}, extraCores...)
	return zap.New(zapcore.NewTee(cores...))
}

// NewSlog bridges a zap logger to *slog.Logger for consumers that require slog
// (e.g. Wails Options.Logger), so all output flows through the same zap core.
func NewSlogLogger(logger *zap.Logger) *slog.Logger {
	return slog.New(zapslog.NewHandler(logger.Core()))
}

func LogLevelFromEnv() zapcore.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FAIRY_LOG_LEVEL"))) {
	case "debug":
		return zapcore.DebugLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}
