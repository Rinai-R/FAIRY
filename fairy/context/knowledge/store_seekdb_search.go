package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const knowledgeIngestSearchRankedSelectSQL = `
SELECT entry.id, entry.topic, entry.statement, entry.verification_basis,
       entry.confidence_basis_points, entry.source_url, entry.source_title,
       entry.evidence_text, entry.source_fetched_at_ms, entry.updated_at_ms,
       ranked.score
FROM ranked_entries ranked
JOIN knowledge_entries entry ON entry.id = ranked.id
WHERE entry.status = 'verified'
ORDER BY ranked.score DESC, entry.confidence_basis_points DESC,
         entry.updated_at_ms DESC, entry.id ASC
LIMIT ?`

const knowledgeIngestLiteralMatchSQL = `
  SELECT id, 1.0 AS score
  FROM knowledge_entries
  WHERE status = 'verified'
    AND (LOCATE(LOWER(?), LOWER(topic)) > 0 OR LOCATE(LOWER(?), LOWER(statement)) > 0)`

const knowledgeIngestSearchSeekDBSQL = `
WITH matching_entries AS (` + knowledgeIngestLiteralMatchSQL + `
  UNION ALL
  SELECT semantic.id, semantic.fts_score / (1.0 + semantic.fts_score) AS score
  FROM (
    SELECT id, MATCH(topic, statement) AGAINST(? IN NATURAL LANGUAGE MODE) AS fts_score
    FROM knowledge_entries
    WHERE status = 'verified'
      AND MATCH(topic, statement) AGAINST(? IN NATURAL LANGUAGE MODE) > 0
  ) semantic
),
ranked_entries AS (
  SELECT id, MAX(score) AS score
  FROM matching_entries
  GROUP BY id
)` + knowledgeIngestSearchRankedSelectSQL

const knowledgeIngestLiteralSearchSeekDBSQL = `
WITH matching_entries AS (` + knowledgeIngestLiteralMatchSQL + `
),
ranked_entries AS (
  SELECT id, MAX(score) AS score
  FROM matching_entries
  GROUP BY id
)` + knowledgeIngestSearchRankedSelectSQL

func knowledgeIngestSearchUsesLiteralOnly(query string) bool {
	return strings.ContainsAny(query, "%_")
}

// searchForIngestSeekDB is deliberately text-only until the general hybrid
// retrieval task lands. Candidate generation is status-first, and the native
// FULLTEXT branch is combined with a literal branch so tokenizer boundaries do
// not hide exact substrings. Queries that contain LIKE wildcards stay on the
// literal branch: IK FULLTEXT treats `%` and `_` as token separators and would
// otherwise recall records that do not contain the original substring.
func (s *Store) searchForIngestSeekDB(ctx context.Context, query string, limit int) ([]Retrieved, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	searchSQL := knowledgeIngestSearchSeekDBSQL
	args := []any{query, query, query, query, limit}
	if knowledgeIngestSearchUsesLiteralOnly(query) {
		searchSQL = knowledgeIngestLiteralSearchSeekDBSQL
		args = []any{query, query, limit}
	}
	rows, err := s.seekDB.QueryContext(queryCtx, searchSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("searching SeekDB knowledge for ingest: %w", err)
	}
	defer rows.Close()
	results := make([]Retrieved, 0, limit)
	for rows.Next() {
		var (
			record          Retrieved
			confidence      int64
			sourceURL       sql.NullString
			sourceTitle     sql.NullString
			sourceEvidence  sql.NullString
			sourceFetchedAt sql.NullInt64
		)
		if err := rows.Scan(
			&record.ID, &record.Topic, &record.Statement, &record.VerificationBasis,
			&confidence, &sourceURL, &sourceTitle, &sourceEvidence, &sourceFetchedAt,
			&record.UpdatedAtUnixMS, &record.TextScore,
		); err != nil {
			return nil, fmt.Errorf("scanning SeekDB knowledge ingest result: %w", err)
		}
		if confidence < 0 || confidence > 10000 {
			return nil, errors.New("knowledge search confidence is invalid")
		}
		if record.TextScore <= 0 || record.TextScore > 1 {
			return nil, errors.New("knowledge search score is invalid")
		}
		record.Layer = "knowledge"
		record.ConfidenceBasisPoints = uint16(confidence)
		if sourceURL.Valid {
			if !sourceEvidence.Valid || !sourceFetchedAt.Valid {
				return nil, errors.New("knowledge source projection is incomplete")
			}
			source := AssistantSource{
				URL: sourceURL.String, Snippet: sourceEvidence.String,
				Rank: 1, FetchedAtUnixMS: sourceFetchedAt.Int64,
			}
			if sourceTitle.Valid {
				source.Title = sourceTitle.String
			}
			record.Sources = []AssistantSource{source}
		} else if sourceTitle.Valid || sourceEvidence.Valid || sourceFetchedAt.Valid {
			return nil, errors.New("knowledge source projection is inconsistent")
		}
		results = append(results, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SeekDB knowledge ingest results: %w", err)
	}
	return results, nil
}
