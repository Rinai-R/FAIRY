package companion

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"fairy/memory"
)

type retentionKnowledgeStore struct{ calls *atomic.Int64 }

func (s retentionKnowledgeStore) EnqueueKnowledgeIngestSnapshots([]memory.KnowledgeIngestSnapshot) error {
	s.calls.Add(1)
	return nil
}

func (retentionKnowledgeStore) ProcessKnowledgeIngestJobs(int) (int, error) {
	return 0, nil
}

type retentionExtractionStore struct{}

func (retentionExtractionStore) PendingExtractionTurnCount(string) (uint64, error) {
	return 1, nil
}

func (retentionExtractionStore) ClaimExtractionBatch(string, int) (*memory.ExtractionBatchInput, error) {
	return nil, nil
}

func (retentionExtractionStore) FailExtractionBatch(string, string, string, bool) error {
	return nil
}

func (retentionExtractionStore) CommitMemoryMutations(string, string, []string, []memory.MemoryMutation) ([]memory.MemoryMutationResult, error) {
	return nil, nil
}

func (retentionExtractionStore) ProcessEmbeddingJobsWithVectorIndex(context.Context, memory.SemanticEmbedder, memory.VectorIndex, int) (memory.EmbeddingJobResult, error) {
	return memory.EmbeddingJobResult{}, nil
}

func TestRetentionEngineKnowledgeIngestIsAsyncAndTracked(t *testing.T) {
	var calls atomic.Int64
	service := &CompanionService{}
	service.memory.retention.knowledge = retentionKnowledgeStore{calls: &calls}
	engine := newRetentionEngine(service)
	engine.scheduleKnowledgeIngest([]memory.KnowledgeIngestSnapshot{{ConversationID: "conversation"}})

	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatal("knowledge ingest did not run")
	}
	deadline = time.Now().Add(time.Second)
	for engine.activeJobs() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if engine.activeJobs() != 0 {
		t.Fatalf("active jobs = %d", engine.activeJobs())
	}
}

func TestRetentionEngineCloseRejectsNewWork(t *testing.T) {
	var calls atomic.Int64
	service := &CompanionService{}
	service.memory.retention.knowledge = retentionKnowledgeStore{calls: &calls}
	engine := newRetentionEngine(service)
	engine.close()
	engine.scheduleKnowledgeIngest([]memory.KnowledgeIngestSnapshot{{ConversationID: "conversation"}})
	time.Sleep(10 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("closed retention engine accepted new work")
	}
	if engine.run(func() {}) {
		t.Fatal("closed retention engine accepted run")
	}
}

func TestRetentionEngineCloseCancelsIdleExtractionTimer(t *testing.T) {
	service := &CompanionService{}
	service.memory.retention.extraction = retentionExtractionStore{}
	engine := newRetentionEngine(service)
	engine.scheduleExtraction("conversation")
	engine.idleMu.Lock()
	idle := len(engine.idle)
	engine.idleMu.Unlock()
	if idle != 1 {
		t.Fatalf("idle timers = %d, want 1", idle)
	}
	engine.close()
	engine.idleMu.Lock()
	idle = len(engine.idle)
	engine.idleMu.Unlock()
	if idle != 0 {
		t.Fatalf("idle timers after close = %d, want 0", idle)
	}
}
