package companion

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"fairy/memory"
	"fairy/session"
)

type retentionKnowledgeStore struct{ calls *atomic.Int64 }

func (s retentionKnowledgeStore) EnqueueKnowledgeIngestSnapshots([]memory.KnowledgeIngestSnapshot) error {
	s.calls.Add(1)
	return nil
}

func (retentionKnowledgeStore) ProcessKnowledgeIngestJobs(int) (int, error) {
	return 0, nil
}

type retentionExtractionStore struct{ claims *atomic.Int64 }

func (retentionExtractionStore) PendingExtractionTurnCount(string) (uint64, error) {
	return 1, nil
}

func (s retentionExtractionStore) ClaimExtractionBatch(string, int) (*memory.ExtractionBatchInput, error) {
	if s.claims != nil {
		s.claims.Add(1)
	}
	return nil, nil
}

func (retentionExtractionStore) FailExtractionBatch(string, string, string, bool) error {
	return nil
}

func (retentionExtractionStore) CommitMemoryMutations(string, string, []string, []memory.MemoryMutation) ([]memory.MemoryMutationResult, error) {
	return nil, nil
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

func TestRetentionEngineBoundsConcurrentJobs(t *testing.T) {
	engine := newRetentionEngineWithCapacity(&CompanionService{}, 2, 2)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	job := func() {
		started <- struct{}{}
		<-release
	}
	if err := engine.run(job); err != nil {
		t.Fatal(err)
	}
	if err := engine.run(job); err != nil {
		t.Fatal(err)
	}
	<-started
	<-started
	var overflowExecuted atomic.Bool
	err := engine.run(func() { overflowExecuted.Store(true) })
	if !errors.Is(err, errRetentionOverloaded) {
		t.Fatalf("overflow error = %v", err)
	}
	if overflowExecuted.Load() {
		t.Fatal("overload job executed")
	}
	if got := engine.activeJobs(); got != 2 {
		t.Fatalf("active jobs = %d, want 2", got)
	}
	close(release)
	engine.close()
	if got := engine.activeJobs(); got != 0 {
		t.Fatalf("active jobs after close = %d, want 0", got)
	}
}

func TestRetentionEngineCloseWaitsForActiveJobsAndRejectsAdmission(t *testing.T) {
	engine := newRetentionEngineWithCapacity(&CompanionService{}, 1, 1)
	started := make(chan struct{})
	release := make(chan struct{})
	if err := engine.run(func() {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	closed := make(chan struct{})
	go func() {
		engine.close()
		close(closed)
	}()
	deadline := time.Now().Add(time.Second)
	for !engine.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !engine.closed.Load() {
		t.Fatal("Close did not close admission")
	}
	select {
	case <-closed:
		t.Fatal("Close returned before active job exited")
	default:
	}
	if err := engine.run(func() {}); !errors.Is(err, errRetentionClosed) {
		t.Fatalf("post-close run error = %v", err)
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after active job exited")
	}
	if got := engine.activeJobs(); got != 0 {
		t.Fatalf("active jobs after close = %d, want 0", got)
	}
}

func TestRetentionEngineBoundsIdleTimersAndFallsBackToImmediateExtraction(t *testing.T) {
	var claims atomic.Int64
	service := &CompanionService{}
	service.memory.retention.extraction = retentionExtractionStore{claims: &claims}
	engine := newRetentionEngineWithCapacity(service, 1, 2)
	engine.scheduleExtraction("conversation-1")
	engine.scheduleExtraction("conversation-2")
	engine.idleMu.Lock()
	idle := len(engine.idle)
	engine.idleMu.Unlock()
	if idle != 2 {
		t.Fatalf("idle timers = %d, want 2", idle)
	}
	engine.scheduleExtraction("conversation-3")
	deadline := time.Now().Add(time.Second)
	for claims.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if claims.Load() != 1 {
		t.Fatalf("immediate extraction claims = %d, want 1", claims.Load())
	}
	engine.idleMu.Lock()
	idle = len(engine.idle)
	engine.idleMu.Unlock()
	if idle != 2 {
		t.Fatalf("idle timers after saturation = %d, want 2", idle)
	}
	engine.close()
}

func TestRetentionEngineOverloadReachesBackgroundAndDesktopCallers(t *testing.T) {
	service := NewCompanionService()
	var knowledgeCalls atomic.Int64
	service.memory.retention.knowledge = retentionKnowledgeStore{calls: &knowledgeCalls}
	engine := newRetentionEngineWithCapacity(service, 1, 1)
	service.retention = engine
	started := make(chan struct{})
	release := make(chan struct{})
	if err := engine.run(func() {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	service.scheduleKnowledgeIngest([]memory.KnowledgeIngestSnapshot{{ConversationID: "conversation-1"}})
	service.backgroundErrorMu.Lock()
	backgroundErr := service.backgroundError
	service.backgroundErrorMu.Unlock()
	if !errors.Is(backgroundErr, errRetentionOverloaded) {
		t.Fatalf("background error = %v", backgroundErr)
	}
	if err := service.ScheduleDesktopInitiation(DesktopInitiationRequest{}, session.DesktopObservation{}); !errors.Is(err, errRetentionOverloaded) {
		t.Fatalf("desktop overload error = %v", err)
	}
	close(release)
	service.Close()
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
	if err := engine.run(func() {}); !errors.Is(err, errRetentionClosed) {
		t.Fatalf("closed retention engine run error = %v", err)
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
