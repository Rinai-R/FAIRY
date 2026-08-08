package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	coredb "fairy/runtime/database"
	"fairy/runtime/observability"

	"github.com/jackc/pgx/v5"
)

const (
	DefaultHistoryQueueCapacity = 1024
	DefaultLogHistoryLimit      = 10_000
	DefaultTraceHistoryLimit    = 1_000
	DefaultMetricHistoryLimit   = 720
	DefaultHistoryMaxAge        = 7 * 24 * time.Hour
	historyCleanupInterval      = time.Minute
	historyCleanupWrites        = 100
)

var ErrHistoryDatabasePoolEmpty = errors.New("observability history database pool is required")

type historyRecord struct {
	kind         string
	key          string
	recordedAtMS int64
	payload      []byte
}

type Store struct {
	pool          *coredb.Pool
	records       chan historyRecord
	stop          chan struct{}
	done          chan struct{}
	enqueueMu     sync.RWMutex
	closeOnce     sync.Once
	stopped       atomic.Bool
	queued        atomic.Uint64
	queueDropped  atomic.Uint64
	writeFailed   atomic.Uint64
	cleanupFailed atomic.Uint64
	keySequence   atomic.Uint64
	limits        map[string]int
	maxAge        time.Duration
	writeRecord   func(historyRecord) error
}

func New(pool *coredb.Pool) (*Store, error) {
	if pool == nil || pool.Raw() == nil {
		return nil, ErrHistoryDatabasePoolEmpty
	}
	store := &Store{
		pool:    pool,
		records: make(chan historyRecord, DefaultHistoryQueueCapacity),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		limits: map[string]int{
			"log": DefaultLogHistoryLimit, "trace": DefaultTraceHistoryLimit, "metric": DefaultMetricHistoryLimit,
		},
		maxAge: DefaultHistoryMaxAge,
	}
	go store.run()
	return store, nil
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
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var payload []byte
	err := s.pool.Raw().QueryRow(queryCtx, `SELECT payload FROM observability_records WHERE kind = 'trace' AND record_key = $1`, traceID).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return observability.MessageTraceDetail{}, false, nil
	}
	if err != nil {
		return observability.MessageTraceDetail{}, false, fmt.Errorf("querying observability trace: %w", err)
	}
	var detail observability.MessageTraceDetail
	if err := json.Unmarshal(payload, &detail); err != nil {
		return observability.MessageTraceDetail{}, false, fmt.Errorf("decoding observability trace: %w", err)
	}
	return detail, true, nil
}

func (s *Store) TracesByMessageID(ctx context.Context, messageID string, limit int) ([]observability.MessageTraceDetail, error) {
	if s == nil || messageID == "" || limit < 1 {
		return []observability.MessageTraceDetail{}, nil
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, `
SELECT payload FROM observability_records
WHERE kind = 'trace' AND payload->>'messageId' = $1
ORDER BY recorded_at_ms DESC, id DESC
LIMIT $2`, messageID, limit)
	if err != nil {
		return nil, fmt.Errorf("querying observability traces by message id: %w", err)
	}
	defer rows.Close()
	details := make([]observability.MessageTraceDetail, 0, limit)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scanning observability trace by message id: %w", err)
		}
		var detail observability.MessageTraceDetail
		if err := json.Unmarshal(payload, &detail); err != nil {
			return nil, fmt.Errorf("decoding observability trace by message id: %w", err)
		}
		details = append(details, detail)
	}
	return details, rows.Err()
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
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	rows, err := s.pool.Raw().Query(queryCtx, `
SELECT payload FROM observability_records
WHERE kind = $1
ORDER BY recorded_at_ms DESC, id DESC
LIMIT $2`, kind, limit)
	if err != nil {
		return fmt.Errorf("querying %s observability history: %w", kind, err)
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return fmt.Errorf("scanning %s observability history: %w", kind, err)
		}
		if err := decode(payload); err != nil {
			return fmt.Errorf("decoding %s observability history: %w", kind, err)
		}
	}
	return rows.Err()
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
		close(s.stop)
		s.enqueueMu.Unlock()
		<-s.done
	})
}

func (s *Store) run() {
	defer close(s.done)
	lastCleanup := time.Now()
	writes := 0
	write := func(record historyRecord) {
		err := s.persistRecord(record)
		if err != nil {
			s.writeFailed.Add(1)
			return
		}
		writes++
		if writes >= historyCleanupWrites || time.Since(lastCleanup) >= historyCleanupInterval {
			if err := s.cleanup(context.Background()); err != nil {
				s.cleanupFailed.Add(1)
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
	ctx, cancel := s.pool.QueryContext(context.Background())
	defer cancel()
	_, err := s.pool.Raw().Exec(ctx, `
INSERT INTO observability_records (kind, record_key, recorded_at_ms, payload, created_at_ms)
VALUES ($1, $2, $3, $4::jsonb, $5)
ON CONFLICT (kind, record_key) DO UPDATE
SET recorded_at_ms = EXCLUDED.recorded_at_ms, payload = EXCLUDED.payload`,
		record.kind, record.key, record.recordedAtMS, record.payload, time.Now().UnixMilli())
	return err
}

func (s *Store) cleanup(ctx context.Context) error {
	cutoff := time.Now().Add(-s.maxAge).UnixMilli()
	for kind, limit := range s.limits {
		queryCtx, cancel := s.pool.QueryContext(ctx)
		_, err := s.pool.Raw().Exec(queryCtx, `
DELETE FROM observability_records
WHERE kind = $1 AND (
  recorded_at_ms < $2 OR id IN (
    SELECT id FROM observability_records
    WHERE kind = $1
    ORDER BY recorded_at_ms DESC, id DESC
    OFFSET $3
  )
)`, kind, cutoff, limit)
		cancel()
		if err != nil {
			return fmt.Errorf("cleaning %s observability history: %w", kind, err)
		}
	}
	return nil
}
