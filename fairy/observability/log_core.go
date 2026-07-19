package observability

import (
	"encoding/json"
	"fmt"

	"go.uber.org/zap/zapcore"
)

// LogCore adapts zap entries into the public bounded LogStore.
type LogCore struct {
	store  *LogStore
	level  zapcore.LevelEnabler
	fields []zapcore.Field
}

func NewLogCore(store *LogStore, level zapcore.LevelEnabler) zapcore.Core {
	return &LogCore{store: store, level: level}
}

func (c *LogCore) Enabled(level zapcore.Level) bool {
	return c != nil && c.store != nil && c.level.Enabled(level)
}

func (c *LogCore) With(fields []zapcore.Field) zapcore.Core {
	clone := *c
	clone.fields = append(append([]zapcore.Field(nil), c.fields...), fields...)
	return &clone
}

func (c *LogCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c *LogCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	all := append(append([]zapcore.Field(nil), c.fields...), fields...)
	normalized := make([]FieldInput, 0, len(all))
	for _, field := range all {
		if field.Key == "" {
			continue
		}
		normalized = append(normalized, FieldInput{Key: field.Key, Value: zapFieldString(field)})
	}
	c.store.Append(EntryInput{
		Time:    entry.Time,
		Level:   entry.Level.String(),
		Logger:  entry.LoggerName,
		Message: entry.Message,
		Fields:  normalized,
	})
	return nil
}

func (c *LogCore) Sync() error { return nil }

func zapFieldString(field zapcore.Field) string {
	encoder := zapcore.NewMapObjectEncoder()
	field.AddTo(encoder)
	value, ok := encoder.Fields[field.Key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case error:
		return typed.Error()
	case fmt.Stringer:
		return typed.String()
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}
