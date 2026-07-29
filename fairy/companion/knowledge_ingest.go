package companion

import (
	"fairy/memory"
)

func cloneWebSearchBatches(batches []webSearchBatch) []webSearchBatch {
	cloned := make([]webSearchBatch, len(batches))
	for index, batch := range batches {
		cloned[index] = batch
		cloned[index].Sources = append([]webSearchSource(nil), batch.Sources...)
	}
	return cloned
}

func memoryKnowledgeIngestBatches(batch webSearchBatch) []memory.KnowledgeIngestBatch {
	batches := make([]memory.KnowledgeIngestBatch, 0, len(batch.Sources))
	for _, source := range batch.Sources {
		persistedSource := memory.KnowledgeIngestSource{
			ID: source.ID, Title: source.Title, URL: source.URL, Snippet: source.Snippet,
			Rank: source.Rank, FetchedAtUnixMS: source.FetchedAtUnixMS,
		}
		batches = append(batches, memory.KnowledgeIngestBatch{
			ID: webSearchSourceJobID(batch.ID, source.ID), ConversationID: batch.ConversationID, TurnID: batch.TurnID,
			Sources: []memory.KnowledgeIngestSource{persistedSource},
		})
	}
	return batches
}

func (s *CompanionService) persistKnowledgeIngestBatch(batch webSearchBatch) error {
	if s == nil || len(batch.Sources) == 0 {
		return nil
	}
	store := s.memory.retention.knowledge
	if store == nil {
		return ErrTurnRuntimeUnavailable
	}
	if err := store.EnqueueKnowledgeIngestBatches(memoryKnowledgeIngestBatches(batch)); err != nil {
		return err
	}
	if s.retention != nil {
		s.retention.wakeKnowledgeIngest()
	}
	return nil
}
