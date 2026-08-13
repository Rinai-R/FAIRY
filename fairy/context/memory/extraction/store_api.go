package extraction

import (
	"context"
	"errors"

	"fairy/context/memory/personal"
)

func (s *Store) ClaimExtractionBatch(conversationID string, limit int) (*BatchInput, error) {
	return s.ClaimExtractionBatchContext(context.Background(), conversationID, limit)
}

func (s *Store) ClaimExtractionBatchContext(ctx context.Context, conversationID string, limit int) (*BatchInput, error) {
	if s.usesSeekDB() {
		if s.personal == nil {
			return nil, ErrPersonalSettlementPending
		}
		claim, err := s.ClaimExtractionTurnsContext(ctx, conversationID, limit)
		if err != nil || claim == nil {
			return nil, err
		}
		input, err := s.EnrichClaimedBatchContext(ctx, claim)
		if err != nil {
			// Preserve the durable identity so the caller can fail/retry the
			// claim. ExistingMemories is not authoritative while err != nil.
			return &BatchInput{
				BatchID: claim.BatchID, ConversationID: claim.ConversationID,
				CharacterID: claim.CharacterID, Turns: append([]Turn(nil), claim.Turns...),
			}, err
		}
		return input, nil
	}
	if !s.usesPostgres() {
		return nil, ErrStoreBackendUnavailable
	}
	return s.claimExtractionBatchPostgres(ctx, conversationID, limit)
}

// ClaimExtractionTurns claims the next durable SeekDB extraction batch
// without pretending that the personal-memory projection has already loaded.
func (s *Store) ClaimExtractionTurns(conversationID string, limit int) (*ClaimedBatch, error) {
	return s.ClaimExtractionTurnsContext(context.Background(), conversationID, limit)
}

func (s *Store) ClaimExtractionTurnsContext(ctx context.Context, conversationID string, limit int) (*ClaimedBatch, error) {
	if !s.usesSeekDB() {
		return nil, ErrStoreBackendUnavailable
	}
	return s.claimExtractionTurnsSeekDB(ctx, conversationID, limit)
}

// EnrichClaimedBatch resolves the authoritative personal-memory projection
// after a durable SeekDB claim has committed. A successful empty projection
// is distinct from a coordinator-only Store, which fails closed.
func (s *Store) EnrichClaimedBatch(batch *ClaimedBatch) (*BatchInput, error) {
	return s.EnrichClaimedBatchContext(context.Background(), batch)
}

func (s *Store) EnrichClaimedBatchContext(ctx context.Context, batch *ClaimedBatch) (*BatchInput, error) {
	if !s.usesSeekDB() {
		return nil, ErrStoreBackendUnavailable
	}
	if s.personal == nil {
		return nil, ErrPersonalSettlementPending
	}
	if err := validateClaimedBatch(batch); err != nil {
		return nil, err
	}
	remaining := personal.MaxContentRunes
	existing, err := s.personal.RetrieveExtractionProjectionContext(
		ctx, batch.CharacterID, BuildRetrievalProjection(batch.Turns), &remaining,
	)
	if err != nil {
		return nil, err
	}
	return &BatchInput{
		BatchID:          batch.BatchID,
		ConversationID:   batch.ConversationID,
		CharacterID:      batch.CharacterID,
		Turns:            append([]Turn(nil), batch.Turns...),
		ExistingMemories: append([]personal.Retrieved(nil), existing...),
	}, nil
}

// CommitClaimedMemoryMutations settles the exact enriched batch. The complete
// BatchInput is mandatory: batch id alone cannot prove the original Turn set.
func (s *Store) CommitClaimedMemoryMutations(
	batch *BatchInput,
	mutations []Mutation,
) ([]MutationResult, error) {
	return s.CommitClaimedMemoryMutationsContext(context.Background(), batch, mutations)
}

func (s *Store) CommitClaimedMemoryMutationsContext(
	ctx context.Context,
	batch *BatchInput,
	mutations []Mutation,
) ([]MutationResult, error) {
	if s.usesSeekDB() {
		if s.personal == nil {
			return nil, ErrPersonalSettlementPending
		}
		if batch == nil {
			return nil, errors.New("extraction batch input is required")
		}
		return s.commitClaimedMemoryMutationsSeekDB(ctx, batch, mutations)
	}
	if !s.usesPostgres() {
		return nil, ErrStoreBackendUnavailable
	}
	if batch == nil {
		return nil, errors.New("extraction batch input is required")
	}
	allowed := make([]string, 0, len(batch.ExistingMemories))
	for _, item := range batch.ExistingMemories {
		allowed = append(allowed, item.ID)
	}
	return s.commitMemoryMutationsPostgres(
		ctx, batch.BatchID, batch.CharacterID, allowed, mutations,
	)
}

func (s *Store) PendingExtractionTurnCount(conversationID string) (uint64, error) {
	return s.PendingExtractionTurnCountContext(context.Background(), conversationID)
}

func (s *Store) PendingExtractionTurnCountContext(ctx context.Context, conversationID string) (uint64, error) {
	if s.usesSeekDB() {
		return s.pendingExtractionTurnCountSeekDB(ctx, conversationID)
	}
	if !s.usesPostgres() {
		return 0, ErrStoreBackendUnavailable
	}
	return s.pendingExtractionTurnCountPostgres(ctx, conversationID)
}

func (s *Store) FailExtractionBatch(batchID, code, message string, retryable bool) error {
	return s.FailExtractionBatchContext(context.Background(), batchID, code, message, retryable)
}

func (s *Store) FailExtractionBatchContext(ctx context.Context, batchID, code, message string, retryable bool) error {
	if s.usesSeekDB() {
		return s.failExtractionBatchSeekDB(ctx, batchID, code, message, retryable)
	}
	if !s.usesPostgres() {
		return ErrStoreBackendUnavailable
	}
	return s.failExtractionBatchPostgres(ctx, batchID, code, message, retryable)
}

func (s *Store) CompleteExtractionBatch(batchID string) error {
	return s.CompleteExtractionBatchContext(context.Background(), batchID)
}

func (s *Store) CompleteExtractionBatchContext(ctx context.Context, batchID string) error {
	if s.usesSeekDB() {
		return ErrPersonalSettlementPending
	}
	if !s.usesPostgres() {
		return ErrStoreBackendUnavailable
	}
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
	if s.usesSeekDB() {
		return nil, ErrPersonalSettlementPending
	}
	if !s.usesPostgres() {
		return nil, ErrStoreBackendUnavailable
	}
	return s.commitMemoryMutationsPostgres(ctx, batchID, characterID, allowedMemoryIDs, mutations)
}
