package companion

import (
	"testing"
	"time"

	"fairy/config"
	"fairy/memory"
)

type blockingKnowledgeMemory struct {
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (m blockingKnowledgeMemory) EnqueueKnowledgeIngestBatches([]memory.KnowledgeIngestBatch) error {
	close(m.started)
	<-m.release
	return nil
}

func (m blockingKnowledgeMemory) ClaimKnowledgeIngestBatches(int) ([]memory.KnowledgeIngestClaim, error) {
	close(m.done)
	return nil, nil
}

func (blockingKnowledgeMemory) CommitKnowledgeIngestBatch(string, string, []memory.KnowledgeIngestFact) (int, error) {
	return 0, nil
}

func (blockingKnowledgeMemory) FailKnowledgeIngestBatch(string, string) error {
	return nil
}

func (blockingKnowledgeMemory) DropKnowledgeIngestBatch(string, string) error {
	return nil
}

type knowledgeIngestProfile struct{}

func (knowledgeIngestProfile) Current() (*config.ProfileSnapshot, error) { return nil, nil }

func TestKnowledgeIngestBatchesPromoteOnlyStableCategoriesWithoutRawQuery(t *testing.T) {
	hits := []WebSearchHit{{Title: "作品条目", URL: "https://example.test/work", Snippet: "这是一段来自公开来源并且长度足够的作品设定摘要。"}}
	for _, test := range []struct {
		query    string
		category string
	}{
		{query: "某部动漫的角色设定", category: "anime"},
		{query: "某款 game 的世界观", category: "game"},
		{query: "这本小说的作者", category: "book"},
	} {
		batch, err := newWebSearchBatch("conversation", "turn", "call-"+test.category, stableKnowledgeCategory(test.query), hits, 1)
		if err != nil {
			t.Fatal(err)
		}
		persisted := memoryKnowledgeIngestBatch(batch)
		if persisted.Category != test.category || len(persisted.Sources) != 1 {
			t.Fatalf("query %q batch = %#v", test.query, persisted)
		}
		if persisted.Category == test.query {
			t.Fatalf("raw user query persisted: %#v", persisted)
		}
	}
	batch, err := newWebSearchBatch("conversation", "turn", "call-chat", stableKnowledgeCategory("今天心情如何"), hits, 1)
	if err != nil {
		t.Fatal(err)
	}
	if persisted := memoryKnowledgeIngestBatch(batch); persisted.Category != "" {
		t.Fatalf("unstable query promoted: %#v", persisted)
	}
}

func TestScheduleKnowledgeIngestDoesNotWaitForStorage(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	service := NewCompanionService()
	defer service.Close()
	service.memory = memoryPorts{retention: retentionMemoryPorts{knowledge: blockingKnowledgeMemory{started: started, release: release, done: done}}}
	service.model = &participationModel{draft: `{"action":"silent"}`}
	service.characterLookup = participationCharacterLookup{}
	service.profiles = knowledgeIngestProfile{}
	service.cfg = participationConfig{}

	returned := make(chan struct{})
	go func() {
		service.scheduleKnowledgeIngestBatches([]webSearchBatch{{
			ID: "batch", ConversationID: "conversation", TurnID: "turn", Category: "anime",
			Sources: []webSearchSource{{ID: "source", Title: "title", URL: "https://example.test", Snippet: "snippet", Rank: 1}},
		}})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("scheduleKnowledgeIngest blocked on storage")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background ingest did not start")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background ingest did not finish")
	}
}
