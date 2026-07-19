package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"fairy/memory"
	"fairy/model"
)

// Constants match crates/fairy-harness/src/runtime.rs.
const (
	extractionThreshold   uint64 = 6
	extractionBatchLimit         = memory.DefaultExtractionBatchLimit
	extractionIdleSeconds        = 30
	embeddingJobPassLimit        = 8
)

type extractionBatchPromptPayload struct {
	Type  string                      `json:"type"`
	Input memory.ExtractionBatchInput `json:"input"`
}

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

	pending, err := s.memoryStore.PendingExtractionTurnCount(conversationID)
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
	batch, err := s.memoryStore.ClaimExtractionBatch(conversationID, extractionBatchLimit)
	if err != nil {
		s.setBackgroundError(err)
		return
	}
	if batch == nil {
		return
	}
	if err := s.executeExtractionBatch(batch); err != nil {
		if failErr := s.memoryStore.FailExtractionBatch(batch.BatchID, "EXTRACTION_BATCH_FAILED", err.Error(), false); failErr != nil {
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
	events, err := s.modelService.ExecutePrompt(
		model.PromptLaneExtract,
		ExtractInstructions,
		ExtractMaxOutputTokens,
		input,
		model.LaneCacheKey(batch.ConversationID, model.PromptLaneExtract),
	)
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
	_, err = s.memoryStore.CommitMemoryMutations(batch.BatchID, batch.CharacterID, allowed, output.Mutations)
	return err
}

func (s *CompanionService) processEmbeddingJobPass(limit int) (memory.EmbeddingJobResult, error) {
	if s == nil || s.memoryStore == nil || limit <= 0 {
		return memory.EmbeddingJobResult{SemanticStatus: "unavailable"}, nil
	}
	return s.memoryStore.ProcessEmbeddingJobs(s.semanticEmbedder, limit)
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
	payload, err := json.Marshal(struct {
		FairyContextData extractionBatchPromptPayload `json:"fairy_context_data"`
	}{
		FairyContextData: extractionBatchPromptPayload{
			Type:  "extraction_batch",
			Input: batch,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("serializing extraction batch: %w", err)
	}
	return []model.PromptItem{{
		Type:    model.PromptItemContextData,
		Content: string(payload),
	}}, nil
}

func ParseMemoryMutationOutput(raw string) (memory.MemoryMutationOutput, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return memory.MemoryMutationOutput{}, errors.New("extraction model returned empty output")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	decoder.DisallowUnknownFields()
	var output memory.MemoryMutationOutput
	if err := decoder.Decode(&output); err != nil {
		return memory.MemoryMutationOutput{}, errors.New("extraction model did not return strict MemoryMutationOutput JSON")
	}
	if decoder.More() {
		return memory.MemoryMutationOutput{}, errors.New("extraction model returned trailing JSON")
	}
	if len(output.Mutations) > memory.MaxMemoryMutationsPerBatch {
		return memory.MemoryMutationOutput{}, errors.New("extraction batch exceeds memory mutation limit")
	}
	for i := range output.Mutations {
		operation := output.Mutations[i].Operation
		if operation != "create" && operation != "supersede" {
			return memory.MemoryMutationOutput{}, fmt.Errorf("unsupported memory mutation operation %q", operation)
		}
	}
	return output, nil
}
