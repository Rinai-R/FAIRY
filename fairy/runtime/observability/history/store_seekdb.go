package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"fairy/runtime/observability"
)

func (s *Store) persistRecordSeekDB(record historyRecord) error {
	queryCtx, cancel := s.seekDBQueryContext(context.Background())
	defer cancel()
	_, err := s.seekDB.ExecContext(queryCtx, `
INSERT INTO observability_records (kind, record_key, recorded_at_ms, payload, created_at_ms)
VALUES (?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  recorded_at_ms = IF(kind <> 'trace', VALUES(recorded_at_ms), recorded_at_ms),
  payload = IF(kind <> 'trace', VALUES(payload), payload)`,
		record.kind, record.key, record.recordedAtMS, record.payload, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("persisting %s observability history: %w", record.kind, err)
	}
	return nil
}

func (s *Store) queryRecentSeekDB(ctx context.Context, kind string, limit int, decode func([]byte) error) error {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	rows, err := s.seekDB.QueryContext(queryCtx, `
SELECT CAST(payload AS CHAR) FROM observability_records
WHERE kind = ?
ORDER BY recorded_at_ms DESC, record_key DESC
LIMIT ?`, kind, limit)
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
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating %s observability history: %w", kind, err)
	}
	return nil
}

func (s *Store) traceSeekDB(ctx context.Context, traceID string) (observability.MessageTraceDetail, bool, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var payload []byte
	err := s.seekDB.QueryRowContext(queryCtx, `
SELECT CAST(payload AS CHAR) FROM observability_records
WHERE kind = 'trace' AND record_key = ?`, traceID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return observability.MessageTraceDetail{}, false, nil
	}
	if err != nil {
		return observability.MessageTraceDetail{}, false, fmt.Errorf("querying observability trace: %w", err)
	}
	return decodeTracePayload(payload)
}

func (s *Store) tracesByMessageIDSeekDB(ctx context.Context, messageID string, limit int) ([]observability.MessageTraceDetail, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	rows, err := s.seekDB.QueryContext(queryCtx, `
SELECT CAST(payload AS CHAR) FROM observability_records
WHERE kind = 'trace' AND JSON_UNQUOTE(JSON_EXTRACT(payload, '$.messageId')) = ?
ORDER BY recorded_at_ms DESC, record_key DESC
LIMIT ?`, messageID, limit)
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
		detail, _, err := decodeTracePayload(payload)
		if err != nil {
			return nil, err
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating observability traces by message id: %w", err)
	}
	return details, nil
}

func (s *Store) cleanupSeekDB(ctx context.Context) error {
	cutoff := time.Now().Add(-s.maxAge).UnixMilli()
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	for kind, limit := range s.limits {
		if _, err := s.seekDB.ExecContext(queryCtx, `
DELETE FROM observability_records
WHERE kind = ? AND recorded_at_ms < ?`, kind, cutoff); err != nil {
			return fmt.Errorf("cleaning %s observability history: %w", kind, err)
		}
		if _, err := s.seekDB.ExecContext(queryCtx, `
DELETE r FROM observability_records r
LEFT JOIN (
  SELECT record_key
  FROM observability_records
  WHERE kind = ?
  ORDER BY recorded_at_ms DESC, record_key DESC
  LIMIT ?
) kept ON kept.record_key = r.record_key
WHERE r.kind = ? AND kept.record_key IS NULL`, kind, limit, kind); err != nil {
			return fmt.Errorf("cleaning %s observability history: %w", kind, err)
		}
	}
	return nil
}

func decodeTracePayload(payload []byte) (observability.MessageTraceDetail, bool, error) {
	var detail observability.MessageTraceDetail
	if err := json.Unmarshal(payload, &detail); err != nil {
		return observability.MessageTraceDetail{}, false, fmt.Errorf("decoding observability trace: %w", err)
	}
	return detail, true, nil
}
