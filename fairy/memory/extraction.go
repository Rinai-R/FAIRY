package memory

import (
	"context"
	"errors"
	"strings"
)

const DefaultExtractionBatchLimit = 12
const MaxMemoryMutationsPerBatch = 16

type ExtractionTurn struct {
	TurnID           string `json:"turnId"`
	UserMessage      string `json:"userMessage"`
	AssistantMessage string `json:"assistantMessage"`
}

type ExtractionBatchInput struct {
	BatchID          string                    `json:"batchId"`
	ConversationID   string                    `json:"conversationId"`
	CharacterID      string                    `json:"characterId"`
	Turns            []ExtractionTurn          `json:"turns"`
	ExistingMemories []RetrievedPersonalMemory `json:"existingMemories"`
}

type MemoryMutation struct {
	Operation             string      `json:"operation"`
	MemoryID              string      `json:"memoryId,omitempty"`
	Kind                  string      `json:"kind"`
	Scope                 MemoryScope `json:"scope"`
	Content               string      `json:"content"`
	ConfidenceBasisPoints uint16      `json:"confidenceBasisPoints"`
}

type MemoryMutationResult struct {
	Status           string `json:"status"`
	MemoryID         string `json:"memoryId,omitempty"`
	ExistingMemoryID string `json:"existingMemoryId,omitempty"`
}

type MemoryMutationOutput struct {
	Mutations []MemoryMutation `json:"mutations"`
}

// ClaimExtractionBatch claims pending completed turns for background extract.
// Returns nil when there is nothing to claim.
func (s *Store) ClaimExtractionBatch(conversationID string, limit int) (*ExtractionBatchInput, error) {
	return s.ClaimExtractionBatchContext(context.Background(), conversationID, limit)
}

func (s *Store) ClaimExtractionBatchContext(ctx context.Context, conversationID string, limit int) (*ExtractionBatchInput, error) {
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
	mutations []MemoryMutation,
) ([]MemoryMutationResult, error) {
	return s.CommitMemoryMutationsContext(context.Background(), batchID, characterID, allowedMemoryIDs, mutations)
}

func (s *Store) CommitMemoryMutationsContext(
	ctx context.Context,
	batchID string,
	characterID string,
	allowedMemoryIDs []string,
	mutations []MemoryMutation,
) ([]MemoryMutationResult, error) {
	return s.commitMemoryMutationsPostgres(ctx, batchID, characterID, allowedMemoryIDs, mutations)
}

func validateMemoryMutation(mutation *MemoryMutation, characterID string) error {
	if mutation == nil {
		return errors.New("memory mutation is required")
	}
	if mutation.Operation != "create" && mutation.Operation != "supersede" {
		return errors.New("memory mutation operation must be create or supersede")
	}
	if mutation.Operation == "supersede" {
		if err := validateID("memory_id", mutation.MemoryID); err != nil {
			return err
		}
	}
	if err := validateMemoryInput(mutation.Kind, mutation.Scope, mutation.Content, mutation.ConfidenceBasisPoints); err != nil {
		return err
	}
	if strings.TrimSpace(mutation.Content) != mutation.Content {
		return errors.New("memory mutation content must not include leading or trailing whitespace")
	}
	if mutation.Scope.Type == "unassigned_legacy" {
		return errors.New("automatic extraction cannot create or modify legacy relationship memories")
	}
	if mutation.Kind == "relationship" && (mutation.Scope.Type != "character" || mutation.Scope.CharacterID != characterID) {
		return errors.New("relationship mutation does not belong to the current character")
	}
	return nil
}

func normalizeMemoryContent(content string) string {
	return strings.Join(strings.Fields(content), " ")
}
