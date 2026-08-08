package knowledge

import (
	"encoding/json"
	"strings"
	"testing"
)

func validKnowledgeIngestTask() IngestTask {
	return IngestTask{
		ID: "task-1", ConversationID: "conversation-1", TurnID: "turn-1",
		Source: IngestSource{
			ID: "source-1", Title: "作品标题", URL: "https://example.test/item?id=1",
			Snippet: "这是一条足够完整的公开来源摘要。", Rank: 1, FetchedAtUnixMS: 1,
		},
	}
}

func TestValidateKnowledgeIngestTaskBoundsCanonicalSource(t *testing.T) {
	valid := validKnowledgeIngestTask()
	encoded, err := validateKnowledgeIngestTask(valid)
	if err != nil || len(encoded) == 0 || encoded[0] != '{' {
		t.Fatalf("valid task = (%q, %v)", encoded, err)
	}
	var source IngestSource
	if err := json.Unmarshal(encoded, &source); err != nil || source != valid.Source {
		t.Fatalf("encoded source = (%#v, %v)", source, err)
	}
	tests := map[string]func(*IngestTask){
		"empty source":   func(task *IngestTask) { task.Source = IngestSource{} },
		"fragment URL":   func(task *IngestTask) { task.Source.URL += "#fragment" },
		"credential URL": func(task *IngestTask) { task.Source.URL = "https://user:pass@example.test/item" },
		"invalid rank":   func(task *IngestTask) { task.Source.Rank = 0 },
		"long snippet": func(task *IngestTask) {
			task.Source.Snippet = strings.Repeat("长", MaxIngestSnippetRunes+1)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			task := valid
			mutate(&task)
			if _, err := validateKnowledgeIngestTask(task); err == nil {
				t.Fatalf("expected invalid task: %#v", task)
			}
		})
	}
}
