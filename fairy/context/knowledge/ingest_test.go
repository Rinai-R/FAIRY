package knowledge

import (
	"testing"
)

func TestKnowledgeIngestTasksAreTopicAgnosticAndIsolated(t *testing.T) {
	hits := []WebSearchHit{
		{Title: "作品条目", URL: "https://example.test/work", Snippet: "这是一段来自公开来源并且长度足够的作品设定摘要。"},
		{Title: "天气条目", URL: "https://example.test/weather", Snippet: "这是一段来自公开来源并且长度足够的天气记录摘要。"},
	}
	batch, err := NewSearchBatch("conversation", "turn", "call-web", hits, 1)
	if err != nil {
		t.Fatal(err)
	}
	tasks := IngestTasks(batch)
	if len(tasks) != 2 {
		t.Fatalf("task count = %d", len(tasks))
	}
	if tasks[0].Source.ID != batch.Sources[0].ID || tasks[1].Source.ID != batch.Sources[1].ID {
		t.Fatalf("tasks = %#v", tasks)
	}
	if tasks[0].ID == tasks[1].ID || tasks[0].ID != webSearchSourceJobID(batch.ID, batch.Sources[0].ID) {
		t.Fatalf("source task IDs are not deterministic and isolated: %#v", tasks)
	}
	for _, task := range tasks {
		if task.ConversationID != "conversation" || task.TurnID != "turn" || task.Source == (IngestSource{}) {
			t.Fatalf("invalid task projection: %#v", task)
		}
	}
}
