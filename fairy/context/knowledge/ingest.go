package knowledge

func IngestTasks(batch SearchBatch) []IngestTask {
	tasks := make([]IngestTask, 0, len(batch.Sources))
	for _, source := range batch.Sources {
		persistedSource := IngestSource{
			ID: source.ID, Title: source.Title, URL: source.URL, Snippet: source.Snippet,
			Rank: source.Rank, FetchedAtUnixMS: source.FetchedAtUnixMS,
		}
		tasks = append(tasks, IngestTask{
			ID: webSearchSourceJobID(batch.ID, source.ID), ConversationID: batch.ConversationID, TurnID: batch.TurnID,
			Source: persistedSource,
		})
	}
	return tasks
}
