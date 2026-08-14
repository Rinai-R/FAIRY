package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"fairy/runtime/observability"
)

const (
	DefaultHistoryQueueCapacity = 1024
	DefaultLogHistoryLimit      = 10_000
	DefaultTraceHistoryLimit    = 1_000
	DefaultMetricHistoryLimit   = 720
	DefaultHistoryMaxAge        = 7 * 24 * time.Hour
	historyCleanupInterval      = time.Minute
	historyCleanupWrites        = 100
	sinkComponentPersist        = "history persist"
	sinkComponentCleanup        = "history cleanup"
)

var (
	ErrHistorySeekDBConnectionEmpty   = errors.New("observability history SeekDB connection is required")
	ErrHistorySeekDBQueryLimitInvalid = errors.New("observability history SeekDB query limit must be greater than zero")
	ErrHistoryBackendUnavailable      = errors.New("observability history store backend is unavailable")
)

type historyRecord struct {
	kind         string
	key          string
	recordedAtMS int64
	payload      []byte
}

type Store struct {
	seekDB             *sql.DB
	queryLimit         time.Duration
	records            chan historyRecord
	stop               chan struct{}
	done               chan struct{}
	enqueueMu          sync.RWMutex
	closeOnce          sync.Once
	stopped            atomic.Bool
	queued             atomic.Uint64
	queueDropped       atomic.Uint64
	writeFailed        atomic.Uint64
	cleanupFailed      atomic.Uint64
	keySequence        atomic.Uint64
	limits             map[string]int
	maxAge             time.Duration
	writeRecord        func(historyRecord) error
	cleanupRecords     func(context.Context) error
	cleanupAfterWrites int
	cleanupEvery       time.Duration
	sinkReport         atomic.Pointer[sinkReporter]
}

type sinkReporter struct {
	report func(component string, recovered bool, err error)
}

type failureWindow struct {
	component string
	emit      func(component string, recovered bool, err error)
	failed    bool
}

// NewSeekDBStore creates an observability history store whose only authority is SeekDB.
func NewSeekDBStore(database *sql.DB, queryLimit time.Duration) (*Store, error) {
	if database == nil {
		return nil, ErrHistorySeekDBConnectionEmpty
	}
	if queryLimit <= 0 {
		return nil, ErrHistorySeekDBQueryLimitInvalid
	}
	store := &Store{
		seekDB:     database,
		queryLimit: queryLimit,
		records:    make(chan historyRecord, DefaultHistoryQueueCapacity),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
		limits: map[string]int{
			"log": DefaultLogHistoryLimit, "trace": DefaultTraceHistoryLimit, "metric": DefaultMetricHistoryLimit,
		},
		maxAge: DefaultHistoryMaxAge,
	}
	go store.run()
	return store, nil
}

func (s *Store) usesSeekDB() bool { return s != nil && s.seekDB != nil }

func (s *Store) seekDBQueryContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, s.queryLimit)
}

// SetSinkDiagnostics registers a bounded reporter for persist and cleanup
// failure windows. Producers never call it; only the write-queue worker does.
func (s *Store) SetSinkDiagnostics(report func(component string, recovered bool, err error)) {
	if s == nil {
		return
	}
	if report == nil {
		s.sinkReport.Store(nil)
		return
	}
	s.sinkReport.Store(&sinkReporter{report: report})
}

func (s *Store) emitSinkDiagnostic(component string, recovered bool, err error) {
	reporter := s.sinkReport.Load()
	if reporter == nil {
		return
	}
	reporter.report(component, recovered, err)
}

func (w *failureWindow) report(err error) {
	if err == nil {
		if w.failed {
			w.emit(w.component, true, nil)
			w.failed = false
		}
		return
	}
	if w.failed {
		return
	}
	w.failed = true
	w.emit(w.component, false, err)
}

func (s *Store) EnqueueLog(entry observability.LogEntry) bool {
	return s.enqueueJSON("log", fmt.Sprintf("%d", entry.Sequence), entry.TimestampUnixMS, entry)
}

func (s *Store) EnqueueTrace(detail observability.MessageTraceDetail) bool {
	return s.enqueueJSON("trace", detail.TraceID, detail.StartedAtUnixMS, detail)
}

func (s *Store) EnqueueMetric(point observability.MetricHistoryPoint) bool {
	key := fmt.Sprintf("%d-%d-%d", point.ProcessStartedUnixMS, point.TimestampUnixMS, s.keySequence.Add(1))
	return s.enqueueJSON("metric", key, point.TimestampUnixMS, point)
}

func (s *Store) enqueueJSON(kind, key string, recordedAtMS int64, value any) bool {
	if s == nil || key == "" || recordedAtMS <= 0 {
		return false
	}
	payload, err := json.Marshal(value)
	if err != nil {
		s.queueDropped.Add(1)
		return false
	}
	record := historyRecord{kind: kind, key: key, recordedAtMS: recordedAtMS, payload: payload}
	s.enqueueMu.RLock()
	defer s.enqueueMu.RUnlock()
	if s.stopped.Load() {
		return false
	}
	select {
	case s.records <- record:
		s.queued.Add(1)
		return true
	default:
		s.queueDropped.Add(1)
		return false
	}
}

func (s *Store) RecentLogs(ctx context.Context, limit int) ([]observability.LogEntry, error) {
	var entries []observability.LogEntry
	if err := s.queryRecent(ctx, "log", limit, func(payload []byte) error {
		var entry observability.LogEntry
		if err := json.Unmarshal(payload, &entry); err != nil {
			return err
		}
		entries = append(entries, entry)
		return nil
	}); err != nil {
		return nil, err
	}
	slices.Reverse(entries)
	return entries, nil
}

func (s *Store) RecentTraces(ctx context.Context, limit int) ([]observability.MessageTraceDetail, error) {
	var details []observability.MessageTraceDetail
	err := s.queryRecent(ctx, "trace", limit, func(payload []byte) error {
		var detail observability.MessageTraceDetail
		if err := json.Unmarshal(payload, &detail); err != nil {
			return err
		}
		details = append(details, detail)
		return nil
	})
	return details, err
}

func (s *Store) Trace(ctx context.Context, traceID string) (observability.MessageTraceDetail, bool, error) {
	if s == nil || traceID == "" {
		return observability.MessageTraceDetail{}, false, nil
	}
	if !s.usesSeekDB() {
		return observability.MessageTraceDetail{}, false, ErrHistoryBackendUnavailable
	}
	return s.traceSeekDB(ctx, traceID)
}

func (s *Store) TracesByMessageID(ctx context.Context, messageID string, limit int) ([]observability.MessageTraceDetail, error) {
	if s == nil || messageID == "" || limit < 1 {
		return []observability.MessageTraceDetail{}, nil
	}
	if !s.usesSeekDB() {
		return nil, ErrHistoryBackendUnavailable
	}
	return s.tracesByMessageIDSeekDB(ctx, messageID, limit)
}

func (s *Store) RecentMetrics(ctx context.Context, limit int) ([]observability.MetricHistoryPoint, error) {
	var points []observability.MetricHistoryPoint
	if err := s.queryRecent(ctx, "metric", limit, func(payload []byte) error {
		var point observability.MetricHistoryPoint
		if err := json.Unmarshal(payload, &point); err != nil {
			return err
		}
		points = append(points, point)
		return nil
	}); err != nil {
		return nil, err
	}
	slices.Reverse(points)
	return points, nil
}

func (s *Store) queryRecent(ctx context.Context, kind string, limit int, decode func([]byte) error) error {
	if s == nil {
		return nil
	}
	if limit < 1 {
		return nil
	}
	if !s.usesSeekDB() {
		return ErrHistoryBackendUnavailable
	}
	return s.queryRecentSeekDB(ctx, kind, limit, decode)
}

func (s *Store) Stats() observability.HistoryStats {
	if s == nil {
		return observability.HistoryStats{}
	}
	return observability.HistoryStats{
		Queued: s.queued.Load(), QueueDropped: s.queueDropped.Load(),
		WriteFailed: s.writeFailed.Load(), CleanupFailed: s.cleanupFailed.Load(),
	}
}

func (s *Store) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.enqueueMu.Lock()
		s.stopped.Store(true)
		if s.stop != nil {
			close(s.stop)
		}
		s.enqueueMu.Unlock()
		if s.done != nil {
			<-s.done
		}
	})
}

func (s *Store) run() {
	defer close(s.done)
	lastCleanup := time.Now()
	writes := 0
	persistWindow := failureWindow{component: sinkComponentPersist, emit: s.emitSinkDiagnostic}
	cleanupWindow := failureWindow{component: sinkComponentCleanup, emit: s.emitSinkDiagnostic}
	cleanupAfter := historyCleanupWrites
	if s.cleanupAfterWrites > 0 {
		cleanupAfter = s.cleanupAfterWrites
	}
	cleanupEvery := historyCleanupInterval
	if s.cleanupEvery > 0 {
		cleanupEvery = s.cleanupEvery
	}
	write := func(record historyRecord) {
		err := s.persistRecord(record)
		if err != nil {
			s.writeFailed.Add(1)
			persistWindow.report(err)
			return
		}
		persistWindow.report(nil)
		writes++
		if writes >= cleanupAfter || time.Since(lastCleanup) >= cleanupEvery {
			if err := s.cleanup(context.Background()); err != nil {
				s.cleanupFailed.Add(1)
				cleanupWindow.report(err)
			} else {
				cleanupWindow.report(nil)
			}
			writes = 0
			lastCleanup = time.Now()
		}
	}
	for {
		select {
		case record := <-s.records:
			write(record)
		case <-s.stop:
			for {
				select {
				case record := <-s.records:
					write(record)
				default:
					return
				}
			}
		}
	}
}

func (s *Store) persistRecord(record historyRecord) error {
	if s.writeRecord != nil {
		return s.writeRecord(record)
	}
	if !s.usesSeekDB() {
		return ErrHistoryBackendUnavailable
	}
	return s.persistRecordSeekDB(record)
}

func (s *Store) cleanup(ctx context.Context) error {
	if s.cleanupRecords != nil {
		return s.cleanupRecords(ctx)
	}
	if !s.usesSeekDB() {
		return ErrHistoryBackendUnavailable
	}
	return s.cleanupSeekDB(ctx)
}
