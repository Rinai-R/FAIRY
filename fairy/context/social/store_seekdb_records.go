package social

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const socialMemoryRecallKinds = `'episode', 'expression', 'behavior'`

const socialMemoryVisibleStatusSQL = `
  AND kind IN (` + socialMemoryRecallKinds + `)
  AND (status = 'active' OR (status = 'suppressed' AND feedback_quarantined_until_ms <= ?))`

const seekDBSocialMemoryEntrySelect = `
entry.id, entry.character_id, entry.conversation_id, entry.kind, entry.situation, entry.content, entry.recall_cue, entry.status,
entry.source_start_ms, entry.source_end_ms,
entry.feedback_evaluation_count, entry.feedback_adopted_count,
entry.feedback_positive_count, entry.feedback_partial_count, entry.feedback_negative_count,
entry.feedback_score_basis_points, entry.feedback_quarantined_until_ms,
entry.created_at_ms, entry.updated_at_ms`

const socialMemorySearchRankedSelectSQL = `
SELECT ` + seekDBSocialMemoryEntrySelect + `
FROM ranked_entries ranked
JOIN social_memory_entries entry ON entry.id = ranked.id
ORDER BY ranked.score DESC,
         CASE WHEN entry.status = 'active' THEN 0 ELSE 1 END ASC,
         entry.feedback_score_basis_points DESC,
         entry.updated_at_ms DESC,
         entry.id ASC
LIMIT ?`

func socialMemoryLiteralMatchSQL(scopeSQL string) string {
	return `
  SELECT id, 1.0 AS score
  FROM social_memory_entries
  WHERE ` + scopeSQL + socialMemoryVisibleStatusSQL + `
    AND (LOCATE(LOWER(?), LOWER(situation)) > 0
      OR LOCATE(LOWER(?), LOWER(content)) > 0
      OR LOCATE(LOWER(?), LOWER(recall_cue)) > 0)`
}

func socialMemorySearchSQL(scopeSQL string) string {
	return `
WITH matching_entries AS (` + socialMemoryLiteralMatchSQL(scopeSQL) + `
  UNION ALL
  SELECT semantic.id, semantic.fts_score / (1.0 + semantic.fts_score) AS score
  FROM (
    SELECT id, MATCH(situation, content, recall_cue) AGAINST(? IN NATURAL LANGUAGE MODE) AS fts_score
    FROM social_memory_entries
    WHERE ` + scopeSQL + socialMemoryVisibleStatusSQL + `
      AND MATCH(situation, content, recall_cue) AGAINST(? IN NATURAL LANGUAGE MODE) > 0
  ) semantic
),
ranked_entries AS (
  SELECT id, MAX(score) AS score
  FROM matching_entries
  GROUP BY id
)` + socialMemorySearchRankedSelectSQL
}

func socialMemoryLiteralSearchSQL(scopeSQL string) string {
	return `
WITH matching_entries AS (` + socialMemoryLiteralMatchSQL(scopeSQL) + `
),
ranked_entries AS (
  SELECT id, MAX(score) AS score
  FROM matching_entries
  GROUP BY id
)` + socialMemorySearchRankedSelectSQL
}

func socialMemorySearchUsesLiteralOnly(query string) bool {
	return strings.ContainsAny(query, "%_")
}

func (s *Store) storeSocialMemoryEntriesSeekDB(ctx context.Context, input SocialMemoryBatchInput) ([]SocialMemoryEntry, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning SeekDB social memory transaction: %w", err)
	}
	defer tx.Rollback()
	if err := lockSeekDBSocialConversation(queryCtx, tx, input.CharacterID, input.ConversationID); err != nil {
		return nil, err
	}
	now := s.currentUnixMS()
	entries := make([]SocialMemoryEntry, 0, len(input.Entries))
	for _, candidate := range input.Entries {
		entry, err := insertSeekDBSocialMemoryEntry(queryCtx, tx, input.CharacterID, input.ConversationID, candidate, now)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing SeekDB social memory transaction: %w", err)
	}
	return entries, nil
}

func insertSeekDBSocialMemoryEntry(
	ctx context.Context,
	tx seekDBTx,
	characterID, conversationID string,
	candidate SocialMemoryEntryInput,
	now int64,
) (SocialMemoryEntry, error) {
	digest := socialMemoryContentDigest(candidate)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO social_memory_entries(
  id, character_id, conversation_id, kind, situation, content, recall_cue,
  content_hash, sender_id, sender_name, status, source_start_ms, source_end_ms,
  feedback_evaluation_count, feedback_adopted_count, feedback_positive_count,
  feedback_partial_count, feedback_negative_count, feedback_score_basis_points,
  created_at_ms, updated_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, '', 'active', ?, ?, 0, 0, 0, 0, 0, 0, ?, ?)
ON DUPLICATE KEY UPDATE
  updated_at_ms = VALUES(updated_at_ms),
  source_start_ms = LEAST(source_start_ms, VALUES(source_start_ms)),
  source_end_ms = GREATEST(source_end_ms, VALUES(source_end_ms))`,
		newID(), characterID, conversationID, candidate.Kind, candidate.Situation,
		candidate.Content, candidate.RecallCue, digest[:],
		candidate.SourceStartUnixMS, candidate.SourceEndUnixMS, now, now,
	); err != nil {
		return SocialMemoryEntry{}, fmt.Errorf("inserting SeekDB social memory entry: %w", err)
	}
	return scanSeekDBSocialMemoryEntry(tx.QueryRowContext(ctx, `
SELECT `+seekDBSocialMemoryEntryColumns+`
FROM social_memory_entries
WHERE conversation_id = ? AND kind = ? AND content_hash = ?`,
		conversationID, candidate.Kind, digest[:],
	))
}

func (s *Store) retrieveSocialMemoryContextSeekDB(
	ctx context.Context,
	characterID, conversationID, query string,
) (SocialMemoryContext, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	if err := verifySeekDBSocialConversation(queryCtx, s.seekDB, characterID, conversationID, false); err != nil {
		return SocialMemoryContext{}, err
	}
	return querySeekDBSocialMemoryContext(
		queryCtx, s.seekDB, "character_id = ? AND conversation_id = ?",
		[]any{characterID, conversationID}, query, s.currentUnixMS(), 12, 9, 1800,
	)
}

func (s *Store) retrieveCharacterSocialMemoryContextSeekDB(
	ctx context.Context,
	characterID, query string,
) (SocialMemoryContext, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	return querySeekDBSocialMemoryContext(
		queryCtx, s.seekDB, "character_id = ?",
		[]any{characterID}, query, s.currentUnixMS(), 24, 12, 1800,
	)
}

func querySeekDBSocialMemoryContext(
	ctx context.Context,
	database *sql.DB,
	scopeSQL string,
	scopeArgs []any,
	query string,
	now int64,
	sqlLimit, capacity, runeBudget int,
) (SocialMemoryContext, error) {
	searchSQL := socialMemorySearchSQL(scopeSQL)
	args := append([]any{}, scopeArgs...)
	args = append(args, now, query, query, query, query)
	args = append(args, scopeArgs...)
	args = append(args, now, query, sqlLimit)
	if socialMemorySearchUsesLiteralOnly(query) {
		searchSQL = socialMemoryLiteralSearchSQL(scopeSQL)
		args = append([]any{}, scopeArgs...)
		args = append(args, now, query, query, query, sqlLimit)
	}
	rows, err := database.QueryContext(ctx, searchSQL, args...)
	if err != nil {
		return SocialMemoryContext{}, fmt.Errorf("querying SeekDB social memory: %w", err)
	}
	defer rows.Close()
	return collectSeekDBSocialMemoryContext(rows, capacity, runeBudget)
}
