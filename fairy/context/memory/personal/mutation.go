package personal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	coredb "fairy/runtime/database"

	"github.com/jackc/pgx/v5"
)

// FindActiveDuplicate keeps personal-memory identity SQL inside its owner while
// allowing a caller to bind the lookup to an existing transaction.
func FindActiveDuplicate(ctx context.Context, db Querier, kind string, scope Scope, content string) (string, error) {
	scopeKind, characterID, _ := ScopeColumns(scope)
	var rows pgx.Rows
	var err error
	if characterID == nil {
		rows, err = db.Query(ctx, `SELECT id, content FROM personal_memories WHERE kind = $1 AND scope_kind = $2 AND character_id IS NULL AND status = 'active' AND review_status = 'ready' ORDER BY updated_at_ms DESC, id ASC`, kind, scopeKind)
	} else {
		rows, err = db.Query(ctx, `SELECT id, content FROM personal_memories WHERE kind = $1 AND scope_kind = $2 AND character_id = $3 AND status = 'active' AND review_status = 'ready' ORDER BY updated_at_ms DESC, id ASC`, kind, scopeKind, *characterID)
	}
	if err != nil {
		return "", fmt.Errorf("querying duplicate memories: %w", err)
	}
	defer rows.Close()
	normalized := NormalizeContent(content)
	for rows.Next() {
		var id, existing string
		if err := rows.Scan(&id, &existing); err != nil {
			return "", fmt.Errorf("scanning duplicate memory: %w", err)
		}
		if NormalizeContent(existing) == normalized {
			return id, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterating duplicate memories: %w", err)
	}
	return "", nil
}

func RequireActiveScope(ctx context.Context, tx coredb.Transaction, memoryID, kind string, scope Scope) error {
	record, err := ByID(ctx, tx, memoryID, true)
	if err != nil {
		return fmt.Errorf("reading personal memory: %w", err)
	}
	if record.Status != "active" || record.Kind != kind || record.Scope != scope {
		return errors.New("supersede target memory status, kind, or scope does not match")
	}
	return nil
}

func Supersede(ctx context.Context, tx coredb.Transaction, memoryID string, now int64) error {
	changed, err := tx.Exec(ctx, "UPDATE personal_memories SET status = 'superseded', updated_at_ms = $2 WHERE id = $1 AND status = 'active'", memoryID, now)
	if err != nil {
		return fmt.Errorf("superseding personal memory: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("supersede target memory is not active")
	}
	return nil
}

func SetEvidenceIDs(ctx context.Context, tx coredb.Transaction, memoryID string, evidenceIDs []string, now int64) error {
	encoded, err := json.Marshal(evidenceIDs)
	if err != nil {
		return fmt.Errorf("serializing personal memory evidence: %w", err)
	}
	changed, err := tx.Exec(ctx, `UPDATE personal_memories SET evidence_ids_json = $2::jsonb, updated_at_ms = GREATEST(updated_at_ms, $3) WHERE id = $1`, memoryID, encoded, now)
	if err != nil {
		return fmt.Errorf("storing personal memory evidence: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("personal memory does not exist")
	}
	return nil
}
