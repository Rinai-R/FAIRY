package extraction

import "context"

func (s *Store) ClaimExtractionBatch(conversationID string, limit int) (*BatchInput, error) {
	return s.ClaimExtractionBatchContext(context.Background(), conversationID, limit)
}

func (s *Store) ClaimExtractionBatchContext(ctx context.Context, conversationID string, limit int) (*BatchInput, error) {
	return s.claimExtractionBatchPostgres(ctx, conversationID, limit)
}

func (s *Store) PendingExtractionTurnCount(conversationID string) (uint64, error) {
	return s.PendingExtractionTurnCountContext(context.Background(), conversationID)
}

func (s *Store) PendingExtractionTurnCountContext(ctx context.Context, conversationID string) (uint64, error) {
	return s.pendingExtractionTurnCountPostgres(ctx, conversationID)
}

func (s *Store) FailExtractionBatch(batchID, code, message string, retryable bool) error {
	return s.FailExtractionBatchContext(context.Background(), batchID, code, message, retryable)
}

func (s *Store) FailExtractionBatchContext(ctx context.Context, batchID, code, message string, retryable bool) error {
	return s.failExtractionBatchPostgres(ctx, batchID, code, message, retryable)
}

func (s *Store) CompleteExtractionBatch(batchID string) error {
	return s.CompleteExtractionBatchContext(context.Background(), batchID)
}

func (s *Store) CompleteExtractionBatchContext(ctx context.Context, batchID string) error {
	return s.completeExtractionBatchPostgres(ctx, batchID)
}

func (s *Store) CommitMemoryMutations(
	batchID string,
	characterID string,
	allowedMemoryIDs []string,
	mutations []Mutation,
) ([]MutationResult, error) {
	return s.CommitMemoryMutationsContext(context.Background(), batchID, characterID, allowedMemoryIDs, mutations)
}

func (s *Store) CommitMemoryMutationsContext(
	ctx context.Context,
	batchID string,
	characterID string,
	allowedMemoryIDs []string,
	mutations []Mutation,
) ([]MutationResult, error) {
	return s.commitMemoryMutationsPostgres(ctx, batchID, characterID, allowedMemoryIDs, mutations)
}
