package observability

import (
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultLogCapacity      = 2000
	DefaultSubscriberBuffer = 128
	MaxMessageRunes         = 2048
	MaxLoggerRunes          = 128
	MaxFields               = 32
	MaxFieldKeyRunes        = 64
	MaxFieldValueRunes      = 2048
)

const RedactedValue = "[REDACTED]"

var (
	bearerPattern       = regexp.MustCompile(`(?i)bearer\s+\S+`)
	inlineSecretPattern = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|authorization|credential|secret|password|token)(\s*[:=]\s*)\S+`)
)

// LogField is the bounded public representation of a structured zap field.
type LogField struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Truncated bool   `json:"truncated"`
}

// LogEntry is the only log record shape exposed over HTTP and the CLI.
type LogEntry struct {
	Sequence         uint64     `json:"sequence"`
	TimestampUnixMS  int64      `json:"timestampUnixMs"`
	Level            string     `json:"level"`
	Logger           string     `json:"logger"`
	Message          string     `json:"message"`
	MessageTruncated bool       `json:"messageTruncated"`
	Fields           []LogField `json:"fields"`
	FieldsTruncated  bool       `json:"fieldsTruncated"`
}

// FieldInput is normalized before it enters the public ring.
type FieldInput struct {
	Key   string
	Value string
}

// EntryInput is accepted by LogStore.Append and the zap core adapter.
type EntryInput struct {
	Time    time.Time
	Level   string
	Logger  string
	Message string
	Fields  []FieldInput
}

// LogFilter is shared by queries and live subscriptions.
type LogFilter struct {
	MinimumLevel  string
	LoggerPrefix  string
	AfterSequence uint64
	Limit         int
}

// LogSnapshot contains a deterministic query result and store counters.
type LogSnapshot struct {
	Entries         []LogEntry `json:"entries"`
	RetainedEntries uint64     `json:"retainedEntries"`
	DroppedEntries  uint64     `json:"droppedEntries"`
	LatestSequence  uint64     `json:"latestSequence"`
}

// LogStats is embedded in the runtime metrics response.
type LogStats struct {
	RetainedEntries           uint64 `json:"retainedEntries"`
	DroppedEntries            uint64 `json:"droppedEntries"`
	ActiveSubscribers         uint64 `json:"activeSubscribers"`
	SlowSubscriberDisconnects uint64 `json:"slowSubscriberDisconnects"`
}

type subscriber struct {
	filter LogFilter
	ch     chan LogEntry
}

// LogStore owns the bounded log ring and all subscription channels.
type LogStore struct {
	mu                        sync.Mutex
	capacity                  int
	subscriberBuffer          int
	entries                   []LogEntry
	sequence                  uint64
	droppedEntries            uint64
	slowSubscriberDisconnects uint64
	subscribers               map[chan LogEntry]subscriber
	closed                    bool
}

func NewLogStore(capacity int) *LogStore {
	return newLogStore(capacity, DefaultSubscriberBuffer)
}

func newLogStore(capacity, subscriberBuffer int) *LogStore {
	if capacity <= 0 {
		capacity = DefaultLogCapacity
	}
	if subscriberBuffer <= 0 {
		subscriberBuffer = DefaultSubscriberBuffer
	}
	return &LogStore{
		capacity:         capacity,
		subscriberBuffer: subscriberBuffer,
		entries:          make([]LogEntry, 0, capacity),
		subscribers:      make(map[chan LogEntry]subscriber),
	}
}

// Append normalizes input, appends it to the ring, and publishes without blocking.
func (s *LogStore) Append(input EntryInput) LogEntry {
	if s == nil {
		return LogEntry{}
	}
	entry := normalizeEntry(input)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return LogEntry{}
	}
	s.sequence++
	entry.Sequence = s.sequence
	if len(s.entries) == s.capacity {
		copy(s.entries, s.entries[1:])
		s.entries[len(s.entries)-1] = entry
		s.droppedEntries++
	} else {
		s.entries = append(s.entries, entry)
	}
	for ch, sub := range s.subscribers {
		if !matchesLog(entry, sub.filter) {
			continue
		}
		select {
		case ch <- cloneLogEntry(entry):
		default:
			delete(s.subscribers, ch)
			close(ch)
			s.slowSubscriberDisconnects++
		}
	}
	return cloneLogEntry(entry)
}

// Query returns matching entries in ascending sequence order.
func (s *LogStore) Query(filter LogFilter) LogSnapshot {
	if s == nil {
		return LogSnapshot{Entries: []LogEntry{}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := filterEntries(s.entries, filter)
	return LogSnapshot{
		Entries:         entries,
		RetainedEntries: uint64(len(s.entries)),
		DroppedEntries:  s.droppedEntries,
		LatestSequence:  s.sequence,
	}
}

// Subscribe atomically captures backlog and registers the live channel.
func (s *LogStore) Subscribe(filter LogFilter) ([]LogEntry, <-chan LogEntry, func()) {
	if s == nil {
		ch := make(chan LogEntry)
		close(ch)
		return []LogEntry{}, ch, func() {}
	}
	s.mu.Lock()
	backlog := filterEntries(s.entries, filter)
	ch := make(chan LogEntry, s.subscriberBuffer)
	if s.closed {
		close(ch)
		s.mu.Unlock()
		return backlog, ch, func() {}
	}
	s.subscribers[ch] = subscriber{filter: filter, ch: ch}
	s.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			s.mu.Lock()
			if _, ok := s.subscribers[ch]; ok {
				delete(s.subscribers, ch)
				close(ch)
			}
			s.mu.Unlock()
		})
	}
	return backlog, ch, unsubscribe
}

func (s *LogStore) Stats() LogStats {
	if s == nil {
		return LogStats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return LogStats{
		RetainedEntries:           uint64(len(s.entries)),
		DroppedEntries:            s.droppedEntries,
		ActiveSubscribers:         uint64(len(s.subscribers)),
		SlowSubscriberDisconnects: s.slowSubscriberDisconnects,
	}
}

func (s *LogStore) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for ch := range s.subscribers {
		delete(s.subscribers, ch)
		close(ch)
	}
}

func normalizeEntry(input EntryInput) LogEntry {
	timestamp := input.Time
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	logger, _ := truncateRunes(input.Logger, MaxLoggerRunes)
	message, messageTruncated := truncateRunes(sanitizeInline(input.Message), MaxMessageRunes)
	entry := LogEntry{
		TimestampUnixMS:  timestamp.UnixMilli(),
		Level:            strings.ToLower(input.Level),
		Logger:           logger,
		Message:          message,
		MessageTruncated: messageTruncated,
		Fields:           make([]LogField, 0, min(len(input.Fields), MaxFields)),
		FieldsTruncated:  len(input.Fields) > MaxFields,
	}
	for _, field := range input.Fields[:min(len(input.Fields), MaxFields)] {
		key, keyTruncated := truncateRunes(field.Key, MaxFieldKeyRunes)
		value := field.Value
		if isSecretKey(field.Key) {
			value = RedactedValue
		} else {
			value = sanitizeInline(value)
		}
		value, valueTruncated := truncateRunes(value, MaxFieldValueRunes)
		entry.Fields = append(entry.Fields, LogField{Key: key, Value: value, Truncated: keyTruncated || valueTruncated})
	}
	return entry
}

func filterEntries(entries []LogEntry, filter LogFilter) []LogEntry {
	result := make([]LogEntry, 0)
	for _, entry := range entries {
		if matchesLog(entry, filter) {
			result = append(result, cloneLogEntry(entry))
		}
	}
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[len(result)-filter.Limit:]
	}
	return result
}

func matchesLog(entry LogEntry, filter LogFilter) bool {
	if entry.Sequence <= filter.AfterSequence {
		return false
	}
	if filter.LoggerPrefix != "" && !strings.HasPrefix(entry.Logger, filter.LoggerPrefix) {
		return false
	}
	return levelRank(entry.Level) >= levelRank(filter.MinimumLevel)
}

func levelRank(level string) int {
	switch strings.ToLower(level) {
	case "debug":
		return 0
	case "warn":
		return 2
	case "error":
		return 3
	default:
		return 1
	}
}

func sanitizeInline(value string) string {
	value = bearerPattern.ReplaceAllString(value, "Bearer "+RedactedValue)
	return inlineSecretPattern.ReplaceAllString(value, "${1}${2}"+RedactedValue)
}

func isSecretKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
	_, ok := map[string]struct{}{
		"apikey": {}, "accesstoken": {}, "authorization": {}, "credential": {},
		"secret": {}, "password": {}, "token": {},
	}[normalized]
	return ok
}

func truncateRunes(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return string(runes[:limit]), true
}

func cloneLogEntry(entry LogEntry) LogEntry {
	fields := make([]LogField, len(entry.Fields))
	copy(fields, entry.Fields)
	entry.Fields = fields
	return entry
}

// SortedFields is used by adapters that originate from unordered encoders.
func SortedFields(fields map[string]string) []FieldInput {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]FieldInput, 0, len(keys))
	for _, key := range keys {
		result = append(result, FieldInput{Key: key, Value: fields[key]})
	}
	return result
}
