package companion

import (
	"fairy/memory"
)

func memoryKnowledgeIngestTasks(batch webSearchBatch) []memory.KnowledgeIngestTask {
	tasks := make([]memory.KnowledgeIngestTask, 0, len(batch.Sources))
	for _, source := range batch.Sources {
		persistedSource := memory.KnowledgeIngestSource{
			ID: source.ID, Title: source.Title, URL: source.URL, Snippet: source.Snippet,
			Rank: source.Rank, FetchedAtUnixMS: source.FetchedAtUnixMS,
		}
		tasks = append(tasks, memory.KnowledgeIngestTask{
			ID: webSearchSourceJobID(batch.ID, source.ID), ConversationID: batch.ConversationID, TurnID: batch.TurnID,
			Source: persistedSource,
		})
	}
	return tasks
}

func (s *CompanionService) persistKnowledgeIngestTasks(batch webSearchBatch) error {
	if s == nil || len(batch.Sources) == 0 {
		return nil
	}
	store := s.memory.retention.knowledge
	if store == nil {
		return ErrTurnRuntimeUnavailable
	}
	if err := store.EnqueueKnowledgeIngestTasks(memoryKnowledgeIngestTasks(batch)); err != nil {
		return err
	}
	if s.retention != nil {
		s.retention.wakeKnowledgeIngest()
	}
	return nil
}
