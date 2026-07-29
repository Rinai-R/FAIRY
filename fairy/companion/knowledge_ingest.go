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

func memoryKnowledgeIngestBatch(batch webSearchBatch) memory.KnowledgeIngestBatch {
	sources := make([]memory.KnowledgeIngestSource, 0, len(batch.Sources))
	for _, source := range batch.Sources {
		sources = append(sources, memory.KnowledgeIngestSource{
			ID: source.ID, Title: source.Title, URL: source.URL, Snippet: source.Snippet,
			Rank: source.Rank, FetchedAtUnixMS: source.FetchedAtUnixMS,
		})
	}
	return memory.KnowledgeIngestBatch{
		ID: batch.ID, ConversationID: batch.ConversationID, TurnID: batch.TurnID,
		Sources: sources,
	}
}

func (s *CompanionService) persistKnowledgeIngestBatch(batch webSearchBatch) error {
	if s == nil || len(batch.Sources) == 0 {
		return nil
	}
	store := s.memory.retention.knowledge
	if store == nil {
		return ErrTurnRuntimeUnavailable
	}
	if err := store.EnqueueKnowledgeIngestBatches([]memory.KnowledgeIngestBatch{memoryKnowledgeIngestBatch(batch)}); err != nil {
		return err
	}
	if s.retention != nil {
		s.retention.wakeKnowledgeIngest()
	}
	return nil
}
