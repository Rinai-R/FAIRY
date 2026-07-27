package retention

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/memory/semantic"
	"fairy/model"
)

type testHost struct {
	knowledge atomic.Int64
	errors    atomic.Int64
}

func (h *testHost) ExtractionStore() ExtractionStore { return nil }
func (h *testHost) KnowledgeIngestStore() KnowledgeIngestStore {
	return testKnowledgeStore{calls: &h.knowledge}
}
func (*testHost) ActiveCharacter(string) (character.Record, error) { return character.Record{}, nil }
func (*testHost) ModelConnection() (config.ModelConnection, error) {
	return config.ModelConnection{}, nil
}
func (*testHost) ExecuteModel(context.Context, model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	return nil, nil
}
func (*testHost) SemanticEmbedder() semantic.Embedder { return nil }
func (*testHost) VectorIndex() VectorIndex            { return nil }
func (h *testHost) SetBackgroundError(error)          { h.errors.Add(1) }
func (*testHost) ClearBackgroundError()               {}

type testKnowledgeStore struct{ calls *atomic.Int64 }

func (s testKnowledgeStore) EnqueueKnowledgeIngestSnapshots([]memory.KnowledgeIngestSnapshot) error {
	s.calls.Add(1)
	return nil
}
func (testKnowledgeStore) ProcessKnowledgeIngestJobs(int) (int, error) { return 0, nil }

type testExtractionStore struct{}

func (testExtractionStore) PendingExtractionTurnCount(string) (uint64, error) { return 1, nil }
func (testExtractionStore) ClaimExtractionBatch(string, int) (*memory.ExtractionBatchInput, error) {
	return nil, nil
}
func (testExtractionStore) FailExtractionBatch(string, string, string, bool) error { return nil }
func (testExtractionStore) CommitMemoryMutations(string, string, []string, []memory.MemoryMutation) ([]memory.MemoryMutationResult, error) {
	return nil, nil
}
func (testExtractionStore) ProcessEmbeddingJobsWithVectorIndex(context.Context, semantic.Embedder, memory.VectorIndex, int) (memory.EmbeddingJobResult, error) {
	return memory.EmbeddingJobResult{}, nil
}

type testHostWithExtraction struct{ host *testHost }

func (h *testHost) withExtractionStore() *testHostWithExtraction {
	return &testHostWithExtraction{host: h}
}
func (h *testHostWithExtraction) ExtractionStore() ExtractionStore { return testExtractionStore{} }
func (h *testHostWithExtraction) KnowledgeIngestStore() KnowledgeIngestStore {
	return testKnowledgeStore{calls: &h.host.knowledge}
}
func (*testHostWithExtraction) ActiveCharacter(string) (character.Record, error) {
	return character.Record{}, nil
}
func (*testHostWithExtraction) ModelConnection() (config.ModelConnection, error) {
	return config.ModelConnection{}, nil
}
func (*testHostWithExtraction) ExecuteModel(context.Context, model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	return nil, nil
}
func (*testHostWithExtraction) SemanticEmbedder() semantic.Embedder { return nil }
func (*testHostWithExtraction) VectorIndex() VectorIndex            { return nil }
func (h *testHostWithExtraction) SetBackgroundError(error)          { h.host.errors.Add(1) }
func (*testHostWithExtraction) ClearBackgroundError()               {}

func TestEngineKnowledgeIngestIsAsyncAndTracked(t *testing.T) {
	host := &testHost{}
	engine := NewEngine(host)
	engine.ScheduleKnowledgeIngest([]memory.KnowledgeIngestSnapshot{{ConversationID: "conversation"}})
	deadline := time.Now().Add(time.Second)
	for host.knowledge.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if host.knowledge.Load() != 1 {
		t.Fatal("knowledge ingest did not run")
	}
	deadline = time.Now().Add(time.Second)
	for engine.ActiveJobs() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if engine.ActiveJobs() != 0 {
		t.Fatalf("active jobs = %d", engine.ActiveJobs())
	}
}

func TestEngineCloseRejectsNewWork(t *testing.T) {
	host := &testHost{}
	engine := NewEngine(host)
	engine.Close()
	engine.ScheduleKnowledgeIngest([]memory.KnowledgeIngestSnapshot{{ConversationID: "conversation"}})
	time.Sleep(10 * time.Millisecond)
	if host.knowledge.Load() != 0 {
		t.Fatal("closed retention engine accepted new work")
	}
	if engine.Run(func() {}) {
		t.Fatal("closed retention engine accepted Run")
	}
}

func TestEngineCloseCancelsIdleExtractionTimer(t *testing.T) {
	host := (&testHost{}).withExtractionStore()
	engine := NewEngine(host)
	engine.ScheduleExtraction("conversation")
	engine.idleMu.Lock()
	idle := len(engine.idle)
	engine.idleMu.Unlock()
	if idle != 1 {
		t.Fatalf("idle timers = %d, want 1", idle)
	}
	engine.Close()
	engine.idleMu.Lock()
	idle = len(engine.idle)
	engine.idleMu.Unlock()
	if idle != 0 {
		t.Fatalf("idle timers after Close = %d, want 0", idle)
	}
}
