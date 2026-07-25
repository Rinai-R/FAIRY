package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *Store) storeSocialMemoryEntriesPostgres(ctx context.Context, input SocialMemoryBatchInput) ([]SocialMemoryEntry, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("beginning social memory transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := VerifySocialConversationScope(queryCtx, tx, input.CharacterID, input.ConversationID); err != nil {
		return nil, err
	}
	now := nowUnixMS()
	entries := make([]SocialMemoryEntry, 0, len(input.Entries))
	for _, candidate := range input.Entries {
		entry, err := InsertSocialMemoryEntry(queryCtx, tx, newID(), input.CharacterID, input.ConversationID, candidate, now)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("committing social memory transaction: %w", err)
	}
	return entries, nil
}

func (s *Store) retrieveSocialMemoryContextPostgres(ctx context.Context, characterID, conversationID string, queryFragments []string) (SocialMemoryContext, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	if err := VerifySocialConversationScope(queryCtx, s.pool.Raw(), characterID, conversationID); err != nil {
		return SocialMemoryContext{}, err
	}
	return QuerySocialMemoryContext(queryCtx, s.pool.Raw(), characterID, conversationID, queryFragments)
}

func (s *Store) retrieveCharacterSocialMemoryContextPostgres(ctx context.Context, characterID string, queryFragments []string) (SocialMemoryContext, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return QueryCharacterSocialMemoryContext(queryCtx, s.pool.Raw(), characterID, queryFragments)
}

func (s *Store) recordSocialReplyFeedbackPostgres(ctx context.Context, input SocialReplyFeedbackInput) (SocialReplyFeedback, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return SocialReplyFeedback{}, fmt.Errorf("beginning social feedback transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := VerifySocialConversationScope(queryCtx, tx, input.CharacterID, input.ConversationID); err != nil {
		return SocialReplyFeedback{}, err
	}
	feedback, err := RecordSocialReplyFeedback(queryCtx, tx, input, newID(), nowUnixMS(), SocialNegativeSuppressThreshold)
	if err != nil {
		return SocialReplyFeedback{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return SocialReplyFeedback{}, fmt.Errorf("committing social feedback transaction: %w", err)
	}
	return feedback, nil
}

func (s *Store) recentSocialFeedbackSummaryPostgres(ctx context.Context, characterID, conversationID string) (RecentSocialFeedbackSummary, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	if err := VerifySocialConversationScope(queryCtx, s.pool.Raw(), characterID, conversationID); err != nil {
		return RecentSocialFeedbackSummary{}, err
	}
	return QueryRecentSocialFeedbackSummary(queryCtx, s.pool.Raw(), characterID, conversationID, recentSocialFeedbackLimit)
}

func verifySocialConversationScope(ctx context.Context, db RowQuerier, characterID, conversationID string) error {
	return VerifySocialConversationScope(ctx, db, characterID, conversationID)
}

func scanSocialMemoryEntry(row interface{ Scan(dest ...any) error }) (SocialMemoryEntry, error) {
	return ScanSocialMemoryEntry(row)
}

func socialMemoryContentHash(entry SocialMemoryEntryInput) string {
	return SocialMemoryContentHash(entry)
}

func insertSocialMemoryEntryPostgres(ctx context.Context, tx pgx.Tx, id, characterID, conversationID string, candidate SocialMemoryEntryInput, now int64) (SocialMemoryEntry, error) {
	return InsertSocialMemoryEntry(ctx, tx, id, characterID, conversationID, candidate, now)
}

func querySocialMemoryContextPostgres(ctx context.Context, db Querier, characterID, conversationID string, queryFragments []string) (SocialMemoryContext, error) {
	return QuerySocialMemoryContext(ctx, db, characterID, conversationID, queryFragments)
}

func queryCharacterSocialMemoryContextPostgres(ctx context.Context, db Querier, characterID string, queryFragments []string) (SocialMemoryContext, error) {
	return QueryCharacterSocialMemoryContext(ctx, db, characterID, queryFragments)
}

func recordSocialReplyFeedbackPostgres(ctx context.Context, tx pgx.Tx, input SocialReplyFeedbackInput, id string, now int64) (SocialReplyFeedback, error) {
	return RecordSocialReplyFeedback(ctx, tx, input, id, now, SocialNegativeSuppressThreshold)
}

func queryRecentSocialFeedbackSummaryPostgres(ctx context.Context, db Querier, characterID, conversationID string, limit int) (RecentSocialFeedbackSummary, error) {
	return QueryRecentSocialFeedbackSummary(ctx, db, characterID, conversationID, limit)
}
