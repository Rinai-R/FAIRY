package memory

import "testing"

func TestAcceptKnowledgeIngestRequiresStableCategoryAndPublicSource(t *testing.T) {
	tests := []struct {
		name      string
		category  string
		topic     string
		statement string
		url       string
		rank      uint8
		want      bool
	}{
		{name: "anime source", category: "anime", topic: "作品条目", statement: "这是一段长度足够的公开作品设定摘要。", url: "https://example.test/work", rank: 1, want: true},
		{name: "unknown category", category: "chat", topic: "聊天", statement: "这是一段长度足够但不稳定的聊天内容。", url: "https://example.test/chat", rank: 1},
		{name: "missing source", category: "game", topic: "游戏", statement: "这是一段长度足够的游戏知识摘要。", rank: 1},
		{name: "credential url", category: "book", topic: "书", statement: "这是一段长度足够的书籍知识摘要。", url: "https://user:pass@example.test/book", rank: 1},
		{name: "short body", category: "book", topic: "书", statement: "太短", url: "https://example.test/book", rank: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := acceptKnowledgeIngest(test.category, test.topic, test.statement, test.url, test.rank); got != test.want {
				t.Fatalf("acceptKnowledgeIngest() = %v, want %v", got, test.want)
			}
		})
	}
}
