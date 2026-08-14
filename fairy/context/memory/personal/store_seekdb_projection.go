package personal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	memoryretrieval "fairy/context/memory/retrieval"
)

type seekDBProjectionQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// RetrieveExtractionProjectionContext loads the explicit existing-memory
// projection used to enrich a durable claim. An empty result means the
// authority was queried successfully, never that projection was skipped.
func (s *Store) RetrieveExtractionProjectionContext(
	ctx context.Context,
	characterID string,
	projection []string,
	remaining *int,
) ([]Retrieved, error) {
	if remaining == nil || *remaining < 0 {
		return nil, errors.New("extraction retrieval budget is invalid")
	}
	if !s.usesSeekDB() {
		return nil, ErrStoreBackendUnavailable
	}
	return s.retrieveExtractionProjectionSeekDB(ctx, characterID, projection, remaining)
}

func (s *Store) retrieveExtractionProjectionSeekDB(
	ctx context.Context,
	characterID string,
	projection []string,
	remaining *int,
) ([]Retrieved, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	return retrieveExtractionProjectionSeekDB(queryCtx, s.seekDB, characterID, projection, remaining)
}

// RetrieveExtractionProjectionSeekDBTx recomputes the authoritative personal
// projection inside the caller's already-open settlement transaction. It uses
// the caller's context directly and never opens a fallback authority, deadline
// context, or nested transaction.
func (s *Store) RetrieveExtractionProjectionSeekDBTx(
	ctx context.Context,
	tx *sql.Tx,
	characterID string,
	projection []string,
	remaining *int,
) ([]Retrieved, error) {
	if err := s.requireSeekDBTx(tx); err != nil {
		return nil, err
	}
	return retrieveExtractionProjectionSeekDB(ctx, tx, characterID, projection, remaining)
}

func retrieveExtractionProjectionSeekDB(
	ctx context.Context,
	database seekDBProjectionQuerier,
	characterID string,
	projection []string,
	remaining *int,
) ([]Retrieved, error) {
	if remaining == nil || *remaining < 0 {
		return nil, errors.New("extraction retrieval budget is invalid")
	}
	if err := validateSeekDBID("character_id", characterID); err != nil {
		return nil, err
	}
	queries := make([]string, 0, len(projection))
	projectionRunes := 0
	for _, fragment := range projection {
		normalized, err := memoryretrieval.NormalizePostgresQuery(fragment)
		if err != nil {
			return nil, err
		}
		if normalized == "" {
			continue
		}
		if len(queries) > 0 {
			projectionRunes++
		}
		projectionRunes += len([]rune(normalized))
		if projectionRunes > memoryretrieval.MaxQueryRunes {
			return nil, errors.New("extraction retrieval projection exceeds query budget")
		}
		queries = append(queries, normalized)
	}
	if len(queries) == 0 {
		return []Retrieved{}, nil
	}
	queryText := strings.Join(queries, " ")
	rows, err := database.QueryContext(ctx, `
SELECT id, kind, scope_kind, character_id, content,
       confidence_basis_points, updated_at_ms,
       MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE) AS text_score
FROM personal_memories
WHERE status = 'active' AND review_status = 'ready'
  AND (scope_kind = 'global' OR (scope_kind = 'character' AND character_id = ?))
  AND MATCH(content) AGAINST(? IN NATURAL LANGUAGE MODE) > 0
ORDER BY text_score DESC, confidence_basis_points DESC, updated_at_ms DESC, id ASC
LIMIT 64`, queryText, characterID, queryText)
	if err != nil {
		return nil, fmt.Errorf("querying SeekDB extraction existing personal memories: %w", err)
	}
	defer rows.Close()
	perKind := make(map[string]int)
	results := make([]Retrieved, 0)
	for rows.Next() {
		var (
			record     Retrieved
			scopeKind  string
			character  sql.NullString
			confidence int64
			textScore  float64
		)
		if err := rows.Scan(
			&record.ID, &record.Kind, &scopeKind, &character, &record.Content,
			&confidence, &record.UpdatedAtUnixMS, &textScore,
		); err != nil {
			return nil, fmt.Errorf("scanning SeekDB extraction personal memory: %w", err)
		}
		if perKind[record.Kind] >= MaxResultsPerKind {
			continue
		}
		if confidence < 0 || confidence > 10000 {
			return nil, errors.New("retrieved personal memory confidence is invalid")
		}
		if err := ValidatePersistedContent(record.ID, record.Content); err != nil {
			return nil, err
		}
		length := len([]rune(record.Content))
		if length > *remaining {
			continue
		}
		*remaining -= length
		perKind[record.Kind]++
		record.Scope = Scope{Type: scopeKind}
		if character.Valid {
			record.Scope.CharacterID = character.String
		}
		record.Layer = Layer(record.Kind, record.Scope)
		record.ConfidenceBasisPoints = uint16(confidence)
		record.TextScore = textScore
		results = append(results, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SeekDB extraction personal memories: %w", err)
	}
	return results, nil
}
