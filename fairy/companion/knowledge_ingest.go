package companion

import (
	"strings"

	"fairy/memory"
	"fairy/search"
)

func knowledgeIngestSnapshots(conversationID, turnID, query string, hits []search.Hit, fetchedAtUnixMS int64) []memory.KnowledgeIngestSnapshot {
	category := stableKnowledgeCategory(query)
	if category == "" {
		return nil
	}
	snapshots := make([]memory.KnowledgeIngestSnapshot, 0, len(hits))
	for index, hit := range hits {
		snapshots = append(snapshots, memory.KnowledgeIngestSnapshot{
			ConversationID:  conversationID,
			TurnID:          turnID,
			Query:           category,
			Title:           hit.Title,
			URL:             hit.URL,
			Snippet:         hit.Snippet,
			Rank:            uint8(index + 1),
			FetchedAtUnixMS: fetchedAtUnixMS,
		})
	}
	return snapshots
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

// scheduleKnowledgeIngest enqueues retrieval snapshots and processes a bounded
// batch asynchronously. Writes verified knowledge with no human Confirm.
func (s *CompanionService) scheduleKnowledgeIngest(snapshots []memory.KnowledgeIngestSnapshot) {
	if s == nil || !s.RespondRuntimeMigrated() || len(snapshots) == 0 {
		return
	}
	s.backgroundJobs.Add(1)
	go func() {
		defer s.backgroundJobs.Add(-1)
		if err := s.memory.EnqueueKnowledgeIngestSnapshots(snapshots); err != nil {
			s.setBackgroundError(err)
			return
		}
		if _, err := s.memory.ProcessKnowledgeIngestJobs(8); err != nil {
			s.setBackgroundError(err)
		}
	}()
}
