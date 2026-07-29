package memory

import (
	"strings"
	"testing"
)

func TestAcceptKnowledgeIngestRequiresValidFactAndPublicSource(t *testing.T) {
	tests := []struct {
		name      string
		topic     string
		statement string
		url       string
		rank      uint8
		want      bool
	}{
		{name: "public source", topic: "作品条目", statement: "这是一段长度足够的公开作品设定摘要。", url: "https://example.test/work", rank: 1, want: true},
		{name: "topic agnostic", topic: "聊天", statement: "这是一段长度足够且来源公开的聊天内容。", url: "https://example.test/chat", rank: 1, want: true},
		{name: "missing source", topic: "游戏", statement: "这是一段长度足够的游戏知识摘要。", rank: 1},
		{name: "credential url", topic: "书", statement: "这是一段长度足够的书籍知识摘要。", url: "https://user:pass@example.test/book", rank: 1},
		{name: "short body", topic: "书", statement: "太短", url: "https://example.test/book", rank: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := acceptKnowledgeIngest(test.topic, test.statement, test.url, test.rank); got != test.want {
				t.Fatalf("acceptKnowledgeIngest() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidateKnowledgeIngestBatchBoundsCanonicalSources(t *testing.T) {
	valid := KnowledgeIngestBatch{
		ID: "batch-1", ConversationID: "conversation-1", TurnID: "turn-1",
		Sources: []KnowledgeIngestSource{{
			ID: "source-1", Title: "作品标题", URL: "https://example.test/item?id=1",
			Snippet: "这是一条足够完整的公开来源摘要。", Rank: 1, FetchedAtUnixMS: 1,
		}},
	}
	encoded, err := validateKnowledgeIngestBatch(valid)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("valid batch = (%q, %v)", encoded, err)
	}
	tests := map[string]func(*KnowledgeIngestBatch){
		"empty sources":  func(batch *KnowledgeIngestBatch) { batch.Sources = nil },
		"fragment URL":   func(batch *KnowledgeIngestBatch) { batch.Sources[0].URL += "#fragment" },
		"credential URL": func(batch *KnowledgeIngestBatch) { batch.Sources[0].URL = "https://user:pass@example.test/item" },
		"invalid rank":   func(batch *KnowledgeIngestBatch) { batch.Sources[0].Rank = 0 },
		"long snippet": func(batch *KnowledgeIngestBatch) {
			batch.Sources[0].Snippet = strings.Repeat("长", MaxKnowledgeIngestSnippetRunes+1)
		},
		"duplicate source": func(batch *KnowledgeIngestBatch) { batch.Sources = append(batch.Sources, batch.Sources[0]) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			batch := valid
			batch.Sources = append([]KnowledgeIngestSource(nil), valid.Sources...)
			mutate(&batch)
			if _, err := validateKnowledgeIngestBatch(batch); err == nil {
				t.Fatalf("expected invalid batch: %#v", batch)
			}
		})
	}
}

func TestEnqueueKnowledgeIngestBatchesRejectsNewMultiSourceJobsBeforeDatabaseAccess(t *testing.T) {
	store := &Store{}
	err := store.enqueueKnowledgeIngestBatchesPostgres(t.Context(), []KnowledgeIngestBatch{{
		ID: "batch-1", ConversationID: "conversation-1", TurnID: "turn-1",
		Sources: []KnowledgeIngestSource{
			{ID: "source-1", Title: "一", URL: "https://example.test/1", Snippet: "来源一", Rank: 1},
			{ID: "source-2", Title: "二", URL: "https://example.test/2", Snippet: "来源二", Rank: 2},
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "exactly one source") {
		t.Fatalf("error = %v", err)
	}
}
