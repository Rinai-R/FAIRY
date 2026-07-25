package telemetry

import (
	"fairy/logx"
	"fairy/observability"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	LogStore               = observability.LogStore
	LogEntry               = observability.LogEntry
	LogField               = observability.LogField
	LogFilter              = observability.LogFilter
	LogSnapshot            = observability.LogSnapshot
	HTTPMetrics            = observability.HTTPMetrics
	HTTPMetricsSnapshot    = observability.HTTPMetricsSnapshot
	MessageMetrics         = observability.MessageMetrics
	MessageMetricsSnapshot = observability.MessageMetricsSnapshot
)

const DefaultLogCapacity = observability.DefaultLogCapacity

var (
	NewLogStore       = observability.NewLogStore
	NewLogCore        = observability.NewLogCore
	NewHTTPMetrics    = observability.NewHTTPMetrics
	NewMessageMetrics = observability.NewMessageMetrics
	LevelFromEnv      = logx.LevelFromEnv
)

// NewLogger builds the process logger, teeing into the shared log store when provided.
func NewLogger(store *LogStore, logger *zap.Logger) *zap.Logger {
	if logger == nil {
		return logx.New(NewLogCore(store, LevelFromEnv()))
	}
	return logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return zapcore.NewTee(core, NewLogCore(store, LevelFromEnv()))
	}))
}
