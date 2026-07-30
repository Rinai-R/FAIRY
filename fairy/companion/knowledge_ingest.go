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
