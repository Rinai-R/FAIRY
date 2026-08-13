package personal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

func (s *Store) requireSeekDBTx(tx *sql.Tx) error {
	if !s.usesSeekDB() {
		return ErrStoreBackendUnavailable
	}
	if tx == nil {
		return ErrSeekDBTransactionEmpty
	}
	return nil
}

type seekDBDuplicateCandidateQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// ListActiveDuplicateCandidateIDsSeekDBContext discovers exact normalized
// duplicates outside the settlement transaction so a caller can avoid an
// unnecessary provider call. The indexed hash bounds the candidate set while
// the original content comparison preserves correctness under hash collision.
// This snapshot is advisory; write transactions must call
// LockActiveDuplicateCandidatesSeekDBTx after locking the mutation guard.
func (s *Store) ListActiveDuplicateCandidateIDsSeekDBContext(
	ctx context.Context,
	kind string,
	scope Scope,
	content string,
) ([]string, error) {
	if !s.usesSeekDB() {
		return nil, ErrStoreBackendUnavailable
	}
	if err := ValidateInput(kind, scope, content, 0); err != nil {
		return nil, err
	}
	if err := validateSeekDBScope(scope); err != nil {
		return nil, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	return listActiveDuplicateCandidateIDsSeekDB(queryCtx, s.seekDB, kind, scope, content)
}

func listActiveDuplicateCandidateIDsSeekDB(
	ctx context.Context,
	database seekDBDuplicateCandidateQuerier,
	kind string,
	scope Scope,
	content string,
) ([]string, error) {
	scopeKind, characterID, _ := ScopeColumns(scope)
	digest := normalizedContentHash(content)
	query := `
SELECT id, content
FROM personal_memories FORCE INDEX (personal_memories_duplicate_revalidation_idx)
WHERE kind = ? AND scope_kind = ? AND character_id IS NULL
  AND review_status = 'ready' AND status = 'active'
  AND normalized_content_hash = ?
ORDER BY updated_at_ms DESC, id ASC`
	arguments := []any{kind, scopeKind, digest[:]}
	if characterID != nil {
		query = `
SELECT id, content
FROM personal_memories FORCE INDEX (personal_memories_duplicate_revalidation_idx)
WHERE kind = ? AND scope_kind = ? AND character_id = ?
  AND review_status = 'ready' AND status = 'active'
  AND normalized_content_hash = ?
ORDER BY updated_at_ms DESC, id ASC`
		arguments = []any{kind, scopeKind, *characterID, digest[:]}
	}
	rows, err := database.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("discovering duplicate SeekDB personal memories: %w", err)
	}
	defer rows.Close()
	normalized := NormalizeContent(content)
	ids := make([]string, 0)
	for rows.Next() {
		var id, existing string
		if err := rows.Scan(&id, &existing); err != nil {
			return nil, fmt.Errorf("scanning duplicate SeekDB personal memory candidate: %w", err)
		}
		if NormalizeContent(existing) == normalized {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating duplicate SeekDB personal memory candidates: %w", err)
	}
	return ids, nil
}

// ListActiveDuplicateCandidateIDsSeekDBTx performs the authoritative indexed
// candidate discovery after the caller has locked personal_memory_write_guard.
// It does not lock personal rows: callers merge these IDs with supplied and
// explicit targets, then acquire the complete set once through
// LockSeekDBRecordsTx's primary-ID order.
func (s *Store) ListActiveDuplicateCandidateIDsSeekDBTx(
	ctx context.Context,
	tx *sql.Tx,
	kind string,
	scope Scope,
	content string,
) ([]string, error) {
	if err := s.requireSeekDBTx(tx); err != nil {
		return nil, err
	}
	if err := ValidateInput(kind, scope, content, 0); err != nil {
		return nil, err
	}
	if err := validateSeekDBScope(scope); err != nil {
		return nil, err
	}
	scopeKind, characterID, _ := ScopeColumns(scope)
	digest := normalizedContentHash(content)
	query := `SELECT id
FROM personal_memories FORCE INDEX (personal_memories_duplicate_revalidation_idx)
WHERE kind = ? AND scope_kind = ? AND character_id IS NULL
  AND review_status = 'ready' AND status = 'active'
  AND normalized_content_hash = ?
ORDER BY id ASC`
	arguments := []any{kind, scopeKind, digest[:]}
	if characterID != nil {
		query = `SELECT id
FROM personal_memories FORCE INDEX (personal_memories_duplicate_revalidation_idx)
WHERE kind = ? AND scope_kind = ? AND character_id = ?
  AND review_status = 'ready' AND status = 'active'
  AND normalized_content_hash = ?
ORDER BY id ASC`
		arguments = []any{kind, scopeKind, *characterID, digest[:]}
	}
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("discovering authoritative duplicate SeekDB personal memories: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning authoritative duplicate SeekDB personal memory id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating authoritative duplicate SeekDB personal memory ids: %w", err)
	}
	return ids, nil
}

func (s *Store) lockSeekDBRecordTx(ctx context.Context, tx *sql.Tx, memoryID string) (Record, error) {
	if err := s.requireSeekDBTx(tx); err != nil {
		return Record{}, err
	}
	if err := validateSeekDBID("memory_id", memoryID); err != nil {
		return Record{}, err
	}
	record, err := selectSeekDBRecord(ctx, tx, memoryID, true)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, errors.New("personal memory does not exist")
	}
	if err != nil {
		return Record{}, fmt.Errorf("locking SeekDB personal memory: %w", err)
	}
	return record, nil
}

// LockSeekDBRecordsTx validates, de-duplicates and sorts the unified explicit
// target and duplicate-candidate IDs before locking them one at a time. The
// returned records are ordered by ID.
func (s *Store) LockSeekDBRecordsTx(ctx context.Context, tx *sql.Tx, memoryIDs []string) ([]Record, error) {
	if err := s.requireSeekDBTx(tx); err != nil {
		return nil, err
	}
	ids := slices.Clone(memoryIDs)
	for _, memoryID := range ids {
		if err := validateSeekDBID("memory_id", memoryID); err != nil {
			return nil, err
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	records := make([]Record, 0, len(ids))
	for _, memoryID := range ids {
		record, err := s.lockSeekDBRecordTx(ctx, tx, memoryID)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Store) RequireActiveScopeSeekDBTx(
	ctx context.Context,
	tx *sql.Tx,
	memoryID, kind string,
	scope Scope,
) error {
	if err := s.requireSeekDBTx(tx); err != nil {
		return err
	}
	if err := validateSeekDBID("memory_id", memoryID); err != nil {
		return err
	}
	record, err := s.lockSeekDBRecordTx(ctx, tx, memoryID)
	if err != nil {
		return fmt.Errorf("reading SeekDB personal memory: %w", err)
	}
	if record.Status != "active" || record.Kind != kind || record.Scope != scope {
		return errors.New("supersede target memory status, kind, or scope does not match")
	}
	return nil
}

func (s *Store) RequireActiveSeekDBTx(ctx context.Context, tx *sql.Tx, memoryID string) error {
	if err := s.requireSeekDBTx(tx); err != nil {
		return err
	}
	if err := validateSeekDBID("memory_id", memoryID); err != nil {
		return err
	}
	record, err := s.lockSeekDBRecordTx(ctx, tx, memoryID)
	if err != nil {
		return fmt.Errorf("reading SeekDB personal memory: %w", err)
	}
	if record.Status != "active" {
		return errors.New("target personal memory is not active")
	}
	return nil
}

func (s *Store) SupersedeSeekDBTx(ctx context.Context, tx *sql.Tx, memoryID string, now int64) error {
	if err := s.requireSeekDBTx(tx); err != nil {
		return err
	}
	if err := validateSeekDBID("memory_id", memoryID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE personal_memories
SET status = 'superseded', updated_at_ms = ?
WHERE id = ? AND status = 'active'`, now, memoryID)
	if err != nil {
		return fmt.Errorf("superseding SeekDB personal memory: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading superseded SeekDB personal memory count: %w", err)
	}
	if rows != 1 {
		return errors.New("supersede target memory is not active")
	}
	return nil
}

func (s *Store) TombstoneSeekDBTx(ctx context.Context, tx *sql.Tx, memoryID string, now int64) error {
	if err := s.requireSeekDBTx(tx); err != nil {
		return err
	}
	if err := validateSeekDBID("memory_id", memoryID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE personal_memories
SET status = 'tombstone', updated_at_ms = ?
WHERE id = ? AND status = 'active'`, now, memoryID)
	if err != nil {
		return fmt.Errorf("tombstoning SeekDB personal memory: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading tombstoned SeekDB personal memory count: %w", err)
	}
	if rows != 1 {
		return errors.New("delete target memory is not active")
	}
	return nil
}

func (s *Store) SetEvidenceIDsSeekDBTx(
	ctx context.Context,
	tx *sql.Tx,
	memoryID string,
	evidenceIDs []string,
	now int64,
) error {
	if err := s.requireSeekDBTx(tx); err != nil {
		return err
	}
	if err := validateSeekDBID("memory_id", memoryID); err != nil {
		return err
	}
	if len(evidenceIDs) > 8 {
		return errors.New("personal memory evidence exceeds limit")
	}
	if evidenceIDs == nil {
		evidenceIDs = []string{}
	}
	for _, evidenceID := range evidenceIDs {
		if err := validateSeekDBEvidenceID(evidenceID); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(evidenceIDs)
	if err != nil {
		return fmt.Errorf("serializing SeekDB personal memory evidence: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE personal_memories
SET evidence_ids = ?, updated_at_ms = GREATEST(updated_at_ms, ?)
WHERE id = ?`, string(encoded), now, memoryID)
	if err != nil {
		return fmt.Errorf("storing SeekDB personal memory evidence: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading SeekDB personal memory evidence count: %w", err)
	}
	if rows == 1 {
		return nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM personal_memories WHERE id = ?", memoryID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return errors.New("personal memory does not exist")
	} else if err != nil {
		return fmt.Errorf("checking SeekDB personal memory evidence target: %w", err)
	}
	return nil
}
