package companion

import (
	"context"
	"sync"
	"testing"
	"time"

	"fairy/config"
	"fairy/memory"
)

type blockingKnowledgeMemory struct {
	started  chan struct{}
	release  chan struct{}
	done     chan struct{}
	doneOnce sync.Once
}

func (m *blockingKnowledgeMemory) EnqueueKnowledgeIngestTasks([]memory.KnowledgeIngestTask) error {
	close(m.started)
	<-m.release
	return nil
}

func (m *blockingKnowledgeMemory) ClaimKnowledgeIngestTasksContext(context.Context, int) ([]memory.KnowledgeIngestClaim, error) {
	m.doneOnce.Do(func() { close(m.done) })
	return nil, nil
}

func (*blockingKnowledgeMemory) KnowledgeIngestLeaseDuration() time.Duration {
	return time.Minute
}

func (*blockingKnowledgeMemory) RenewKnowledgeIngestLeaseContext(context.Context, string) error {
	return nil
}

func (*blockingKnowledgeMemory) ReleaseClaimedKnowledgeIngestJob(string) error {
	return nil
}

func (*blockingKnowledgeMemory) KnowledgeDocumentNeedsExtractionContext(context.Context, string, string, memory.KnowledgeDocument) (bool, error) {
	return true, nil
}

func (*blockingKnowledgeMemory) SearchKnowledgeForIngestContext(context.Context, string, int) ([]memory.RetrievedKnowledge, error) {
	return nil, nil
}

func (*blockingKnowledgeMemory) CommitKnowledgeDocumentActionsContext(context.Context, string, string, memory.KnowledgeDocument, []string, []memory.KnowledgeDocumentAction) (int, error) {
	return 0, nil
}

func (*blockingKnowledgeMemory) FailClaimedKnowledgeIngestJob(string, string) error {
	return nil
}

func (*blockingKnowledgeMemory) RetryClaimedKnowledgeIngestJob(string, string, string) error {
	return nil
}

func (*blockingKnowledgeMemory) DropClaimedKnowledgeIngestJob(string, string) error {
	return nil
}

type knowledgeIngestProfile struct{}

func (knowledgeIngestProfile) Current() (*config.ProfileSnapshot, error) { return nil, nil }

func TestKnowledgeIngestTasksAreTopicAgnostic(t *testing.T) {
	hits := []WebSearchHit{
		{Title: "作品条目", URL: "https://example.test/work", Snippet: "这是一段来自公开来源并且长度足够的作品设定摘要。"},
		{Title: "天气条目", URL: "https://example.test/weather", Snippet: "这是一段来自公开来源并且长度足够的天气记录摘要。"},
	}
	for _, callID := range []string{"call-anime", "call-weather", "call-chat"} {
		batch, err := newWebSearchBatch("conversation", "turn", callID, hits, 1)
		if err != nil {
			t.Fatal(err)
		}
		persisted := memoryKnowledgeIngestTasks(batch)
		if len(persisted) != 2 ||
			persisted[0].Source.ID != batch.Sources[0].ID ||
			persisted[1].Source.ID != batch.Sources[1].ID {
			t.Fatalf("tasks = %#v", persisted)
		}
		if persisted[0].ID == persisted[1].ID || persisted[0].ID != webSearchSourceJobID(batch.ID, batch.Sources[0].ID) {
			t.Fatalf("source job IDs are not deterministic and isolated: %#v", persisted)
		}
	}
}

func TestPersistKnowledgeIngestWaitsForDurableStorage(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	service := NewCompanionService()
	defer service.Close()
	service.memory = memoryPorts{retention: retentionMemoryPorts{knowledge: &blockingKnowledgeMemory{started: started, release: release, done: done}}}
	service.model = &participationModel{draft: `{"action":"silent"}`}
	service.characterLookup = participationCharacterLookup{}
	service.profiles = knowledgeIngestProfile{}
	service.cfg = participationConfig{}

	returned := make(chan struct{})
	go func() {
		_ = service.persistKnowledgeIngestTasks(webSearchBatch{
			ID: "batch", ConversationID: "conversation", TurnID: "turn",
			Sources: []webSearchSource{{ID: "source", Title: "title", URL: "https://example.test", Snippet: "snippet", Rank: 1}},
		})
		close(returned)
	}()
	select {
	case <-returned:
		t.Fatal("persist returned before durable storage completed")
	case <-time.After(25 * time.Millisecond):
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background ingest did not start")
	}
	close(release)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("durable persist did not finish")
	}
}
