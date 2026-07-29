package companion

import (
	"strings"

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
		Category: batch.Category, Sources: sources,
	}
}

func stableKnowledgeCategory(query string) string {
	lower := strings.ToLower(strings.TrimSpace(query))
	categories := []struct {
		name    string
		markers []string
	}{
		{name: "anime", markers: []string{"动漫", "动画", "漫画", "anime", "manga"}},
		{name: "game", markers: []string{"游戏", "game", "攻略", "世界观", "设定集"}},
		{name: "book", markers: []string{"书籍", "小说", "作者", "文学", "novel", "book"}},
	}
	for _, category := range categories {
		for _, marker := range category.markers {
			if strings.Contains(lower, marker) {
				return category.name
			}
		}
	}
	return ""
}

func (s *CompanionService) scheduleKnowledgeIngestBatches(batches []webSearchBatch) {
	if s == nil || s.retention == nil || len(batches) == 0 {
		return
	}
	persisted := make([]memory.KnowledgeIngestBatch, 0, len(batches))
	for _, batch := range batches {
		if batch.Category != "" && len(batch.Sources) > 0 {
			persisted = append(persisted, memoryKnowledgeIngestBatch(batch))
		}
	}
	s.retention.scheduleKnowledgeIngestBatches(persisted)
}
