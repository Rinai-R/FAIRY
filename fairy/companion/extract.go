package companion

import (
	"context"
	"errors"
	"time"

	"fairy/extraction"
	"fairy/memory"
	"fairy/model"
)

const (
	extractionThreshold   = extraction.Threshold
	extractionBatchLimit  = extraction.BatchLimit
	extractionIdleSeconds = extraction.IdleSeconds
	embeddingJobPassLimit = extraction.EmbeddingPassLimit
)

func (s *CompanionService) scheduleBackgroundExtraction(conversationID string) {
	if s == nil || !s.RespondRuntimeMigrated() {
		return
	}
	s.extractionMu.Lock()
	if cancel, ok := s.extractionIdle[conversationID]; ok {
		cancel()
		delete(s.extractionIdle, conversationID)
	}
	s.extractionMu.Unlock()

	pending, err := s.memory.PendingExtractionTurnCount(conversationID)
	if err != nil {
		s.setBackgroundError(err)
		return
	}
	if pending >= extractionThreshold {
		go s.claimAndRunExtraction(conversationID)
		return
	}
	if pending == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.extractionMu.Lock()
	if s.extractionIdle == nil {
		s.extractionIdle = make(map[string]context.CancelFunc)
	}
	s.extractionIdle[conversationID] = cancel
	s.extractionMu.Unlock()
	go func() {
		timer := time.NewTimer(extractionIdleSeconds * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.extractionMu.Lock()
			delete(s.extractionIdle, conversationID)
			s.extractionMu.Unlock()
			s.claimAndRunExtraction(conversationID)
		}
	}()
}

func (s *CompanionService) claimAndRunExtraction(conversationID string) {
	s.backgroundJobs.Add(1)
	defer s.backgroundJobs.Add(-1)
	batch, err := s.memory.ClaimExtractionBatch(conversationID, extractionBatchLimit)
	if err != nil {
		s.setBackgroundError(err)
		return
	}
	if batch == nil {
		return
	}
	if err := s.executeExtractionBatch(batch); err != nil {
		if failErr := s.memory.FailExtractionBatch(batch.BatchID, "EXTRACTION_BATCH_FAILED", err.Error(), false); failErr != nil {
			s.setBackgroundError(failErr)
			return
		}
		s.setBackgroundError(err)
		return
	}
	s.clearBackgroundError()
	if _, err := s.processEmbeddingJobPass(embeddingJobPassLimit); err != nil {
		s.setBackgroundError(err)
	}
}

func (s *CompanionService) executeExtractionBatch(batch *memory.ExtractionBatchInput) error {
	if batch == nil {
		return errors.New("extraction batch is required")
	}
	input, err := BuildExtractInput(*batch)
	if err != nil {
		return err
	}
	record, err := s.activeCharacter(batch.CharacterID)
	if err != nil {
		return err
	}
	connection, err := s.configSource().ModelConnection()
	if err != nil {
		return err
	}
	cacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(batch.ConversationID, model.PromptLaneExtract)
	}
	cacheInput := model.NewCacheKeyInput(model.PromptLaneExtract, connection.Model, batch.ConversationID, ExtractInstructions)
	cacheInput.CharacterRevision = record.Revision
	events, err := s.model.ExecuteRequestContext(context.Background(), model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneExtract, Model: connection.Model, Instructions: ExtractInstructions,
			MaxOutputTokens: ExtractMaxOutputTokens, PromptCacheKey: cacheKey,
		},
		Input: input, CacheInput: &cacheInput,
	})
	if err != nil {
		return err
	}
	output, err := ParseMemoryMutationOutput(collectText(events))
	if err != nil {
		return err
	}
	allowed := make([]string, 0, len(batch.ExistingMemories))
	for _, item := range batch.ExistingMemories {
		allowed = append(allowed, item.ID)
	}
	_, err = s.memory.CommitMemoryMutations(batch.BatchID, batch.CharacterID, allowed, output.Mutations)
	return err
}

func (s *CompanionService) processEmbeddingJobPass(limit int) (memory.EmbeddingJobResult, error) {
	if s == nil || s.memory == nil || limit <= 0 {
		return memory.EmbeddingJobResult{SemanticStatus: "unavailable"}, nil
	}
	return s.memory.ProcessEmbeddingJobsWithVectorIndex(context.Background(), s.semanticEmbedder, s.vectorIndex, limit)
}

func (s *CompanionService) ActiveBackgroundJobs() int64 {
	if s == nil {
		return 0
	}
	return s.backgroundJobs.Load()
}

func (s *CompanionService) setBackgroundError(err error) {
	if s == nil || err == nil {
		return
	}
	s.backgroundErrorMu.Lock()
	s.backgroundError = err
	s.backgroundErrorMu.Unlock()
}

func (s *CompanionService) clearBackgroundError() {
	if s == nil {
		return
	}
	s.backgroundErrorMu.Lock()
	s.backgroundError = nil
	s.backgroundErrorMu.Unlock()
}

func BuildExtractInput(batch memory.ExtractionBatchInput) ([]model.PromptItem, error) {
	return extraction.BuildInput(batch)
}

func ParseMemoryMutationOutput(raw string) (memory.MemoryMutationOutput, error) {
	return extraction.ParseMutationOutput(raw)
}
