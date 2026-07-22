package companion

import (
	"testing"
	"time"

	"fairy/memory"
	"fairy/profile"
	"fairy/search"
)

type blockingKnowledgeMemory struct {
	MemoryPort
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (m blockingKnowledgeMemory) EnqueueKnowledgeIngestSnapshots([]memory.KnowledgeIngestSnapshot) error {
	close(m.started)
	<-m.release
	return nil
}

func (m blockingKnowledgeMemory) ProcessKnowledgeIngestJobs(int) (int, error) {
	close(m.done)
	return 0, nil
}

type knowledgeIngestProfile struct{}

func (knowledgeIngestProfile) Current() (*profile.Snapshot, error) { return nil, nil }

func TestKnowledgeIngestSnapshotsPromotesOnlyStableCategoriesWithoutRawQuery(t *testing.T) {
	hits := []search.Hit{{Title: "作品条目", URL: "https://example.test/work", Snippet: "这是一段来自公开来源并且长度足够的作品设定摘要。"}}
	for _, test := range []struct {
		query    string
		category string
	}{
		{query: "某部动漫的角色设定", category: "anime"},
		{query: "某款 game 的世界观", category: "game"},
		{query: "这本小说的作者", category: "book"},
	} {
		snapshots := knowledgeIngestSnapshots("conversation", "turn", test.query, hits, 1)
		if len(snapshots) != 1 || snapshots[0].Query != test.category {
			t.Fatalf("query %q snapshots = %#v", test.query, snapshots)
		}
		if snapshots[0].Query == test.query {
			t.Fatalf("raw user query persisted: %#v", snapshots[0])
		}
	}
	if snapshots := knowledgeIngestSnapshots("conversation", "turn", "今天心情如何", hits, 1); len(snapshots) != 0 {
		t.Fatalf("unstable query promoted: %#v", snapshots)
	}
}

func TestScheduleKnowledgeIngestDoesNotWaitForStorage(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	service := NewCompanionService()
	defer service.Close()
	service.memory = blockingKnowledgeMemory{started: started, release: release, done: done}
	service.model = &participationModel{draft: `{"action":"silent"}`}
	service.characters = participationCatalog{}
	service.profiles = knowledgeIngestProfile{}
	service.cfg = participationConfig{}

	returned := make(chan struct{})
	go func() {
		service.scheduleKnowledgeIngest([]memory.KnowledgeIngestSnapshot{{ConversationID: "conversation", TurnID: "turn"}})
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
