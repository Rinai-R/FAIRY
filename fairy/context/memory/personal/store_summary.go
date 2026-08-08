package personal

import (
	"context"
	"fmt"
)

func (s *Store) Summary() (Summary, error) {
	return s.SummaryContext(context.Background())
}

func (s *Store) SummaryContext(ctx context.Context) (Summary, error) {
	if s == nil || s.pool == nil || s.pool.Raw() == nil {
		return Summary{}, ErrDatabasePoolEmpty
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
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
		if err := s.pool.Raw().QueryRow(queryCtx, item.query).Scan(item.target); err != nil {
			return Summary{}, fmt.Errorf("loading personal memory summary: %w", err)
		}
	}
	result.ReadOnly = true
	return result, nil
}

func countPostgresScalar(ctx context.Context, store *Store, query string) (int64, error) {
	var count int64
	if err := store.pool.Raw().QueryRow(ctx, query).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
