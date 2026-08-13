package personal

import (
	"context"
	"fmt"
)

func (s *Store) companionPortraitSeekDB(ctx context.Context, characterID string) (Retrieval, error) {
	if err := validateSeekDBID("character_id", characterID); err != nil {
		return Retrieval{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	rows, err := s.seekDB.QueryContext(queryCtx, `
WITH ranked AS (
  SELECT id, kind, scope_kind, character_id, review_status, content, status,
         confidence_basis_points, source_conversation_id, source_turn_id,
         supersedes_id, created_at_ms, updated_at_ms,
         ROW_NUMBER() OVER (
           PARTITION BY kind
           ORDER BY confidence_basis_points DESC, updated_at_ms DESC, id ASC
         ) AS kind_rank
  FROM personal_memories
  WHERE status = 'active' AND review_status = 'ready'
    AND (
      (scope_kind = 'global' AND character_id IS NULL AND kind IN ('profile', 'preference', 'experience'))
      OR (scope_kind = 'character' AND character_id = ? AND kind = 'relationship')
    )
)
SELECT `+seekDBRecordColumns+`
FROM ranked
WHERE kind_rank <= 4
ORDER BY CASE kind WHEN 'profile' THEN 0 WHEN 'preference' THEN 1 WHEN 'relationship' THEN 2 ELSE 3 END,
         confidence_basis_points DESC, updated_at_ms DESC, id ASC
LIMIT ?`, characterID, maxPortraitCandidates)
	if err != nil {
		return Retrieval{}, fmt.Errorf("querying SeekDB companion portrait: %w", err)
	}
	defer rows.Close()
	records := make([]Record, 0, maxPortraitCandidates)
	for rows.Next() {
		record, err := scanSeekDBRecord(rows)
		if err != nil {
			return Retrieval{}, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return Retrieval{}, fmt.Errorf("iterating SeekDB companion portrait: %w", err)
	}
	return buildCompanionPortrait(characterID, records)
}

func (s *Store) summarySeekDB(ctx context.Context) (Summary, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var result Summary
	queries := []struct {
		target *int64
		query  string
	}{
		{&result.Conversations, "SELECT COUNT(*) FROM conversations"},
		{&result.ActiveGlobalMemories, "SELECT COUNT(*) FROM personal_memories WHERE scope_kind = 'global' AND review_status = 'ready' AND status = 'active'"},
		{&result.ActiveCharacterMemories, "SELECT COUNT(*) FROM personal_memories WHERE scope_kind = 'character' AND review_status = 'ready' AND status = 'active'"},
		{&result.NeedsReviewMemories, "SELECT COUNT(*) FROM personal_memories WHERE scope_kind = 'unassigned_legacy' AND review_status = 'needs_review' AND status = 'active'"},
		{&result.PendingExtractionTurns, "SELECT COUNT(*) FROM conversation_turns WHERE status = 'completed' AND extraction_state = 'pending'"},
		{&result.RunningBatches, "SELECT COUNT(DISTINCT extraction_claim_id) FROM conversation_turns WHERE status = 'completed' AND extraction_state = 'claimed'"},
		{&result.FailedBatches, "SELECT COUNT(*) FROM conversation_turns WHERE status = 'completed' AND extraction_state = 'failed'"},
	}
	for _, item := range queries {
		if err := s.seekDB.QueryRowContext(queryCtx, item.query).Scan(item.target); err != nil {
			return Summary{}, fmt.Errorf("loading SeekDB personal memory summary: %w", err)
		}
	}
	result.ReadOnly = true
	return result, nil
}
