package companion

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fairy/memory"
	"fairy/model"
	"fairy/session"
)

type retentionKnowledgeStore struct{ calls *atomic.Int64 }

func (s retentionKnowledgeStore) EnqueueKnowledgeIngestBatches([]memory.KnowledgeIngestBatch) error {
	s.calls.Add(1)
	return nil
}

func (retentionKnowledgeStore) ClaimKnowledgeIngestBatches(int) ([]memory.KnowledgeIngestClaim, error) {
	return nil, nil
}

func (retentionKnowledgeStore) CommitKnowledgeIngestBatch(string, string, []memory.KnowledgeIngestFact) (int, error) {
	return 0, nil
}

func (retentionKnowledgeStore) FailKnowledgeIngestBatch(string, string) error {
	return nil
}

func (retentionKnowledgeStore) DropKnowledgeIngestBatch(string, string) error {
	return nil
}

type capturingKnowledgeIngestStore struct {
	commits []struct {
		jobID   string
		batchID string
		facts   []memory.KnowledgeIngestFact
	}
	drops []string
}

type claimedKnowledgeIngestStore struct {
	claim   memory.KnowledgeIngestClaim
	failed  string
	done    chan struct{}
	claimed atomic.Bool
}

func (*claimedKnowledgeIngestStore) EnqueueKnowledgeIngestBatches([]memory.KnowledgeIngestBatch) error {
	return nil
}

func (s *claimedKnowledgeIngestStore) ClaimKnowledgeIngestBatches(int) ([]memory.KnowledgeIngestClaim, error) {
	if !s.claimed.CompareAndSwap(false, true) {
		return nil, nil
	}
	return []memory.KnowledgeIngestClaim{s.claim}, nil
}

func (*claimedKnowledgeIngestStore) CommitKnowledgeIngestBatch(string, string, []memory.KnowledgeIngestFact) (int, error) {
	return 0, errors.New("unexpected commit")
}

func (s *claimedKnowledgeIngestStore) FailKnowledgeIngestBatch(jobID, _ string) error {
	s.failed = jobID
	close(s.done)
	return nil
}

func (*claimedKnowledgeIngestStore) DropKnowledgeIngestBatch(string, string) error {
	return errors.New("unexpected drop")
}

func (*capturingKnowledgeIngestStore) EnqueueKnowledgeIngestBatches([]memory.KnowledgeIngestBatch) error {
	return nil
}

func (*capturingKnowledgeIngestStore) ClaimKnowledgeIngestBatches(int) ([]memory.KnowledgeIngestClaim, error) {
	return nil, nil
}

func (s *capturingKnowledgeIngestStore) CommitKnowledgeIngestBatch(jobID, batchID string, facts []memory.KnowledgeIngestFact) (int, error) {
	s.commits = append(s.commits, struct {
		jobID   string
		batchID string
		facts   []memory.KnowledgeIngestFact
	}{jobID: jobID, batchID: batchID, facts: append([]memory.KnowledgeIngestFact(nil), facts...)})
	return len(facts), nil
}

func (*capturingKnowledgeIngestStore) FailKnowledgeIngestBatch(string, string) error {
	return nil
}

func (s *capturingKnowledgeIngestStore) DropKnowledgeIngestBatch(jobID, _ string) error {
	s.drops = append(s.drops, jobID)
	return nil
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
	engine.scheduleKnowledgeIngestBatches([]memory.KnowledgeIngestBatch{{ID: "batch", ConversationID: "conversation"}})

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

func TestKnowledgeIngestUsesOneStrictModelCallPerIsolatedBatch(t *testing.T) {
	modelPort := &participationModel{drafts: []string{
		`{"facts":[{"topic":"作品甲","statement":"作品甲将在十月正式公开新篇章。","confidenceBasisPoints":8200,"sourceHitIDs":["source-a"]}]}`,
		`{"facts":[{"topic":"作品乙","statement":"作品乙的续作已经正式公布制作决定。","confidenceBasisPoints":8100,"sourceHitIDs":["source-b"]}]}`,
	}}
	service := &CompanionService{model: modelPort, cfg: participationConfig{}}
	engine := newRetentionEngine(service)
	store := &capturingKnowledgeIngestStore{}
	claims := []memory.KnowledgeIngestClaim{
		{
			JobID: "job-a",
			Batch: memory.KnowledgeIngestBatch{
				ID: "batch-a", ConversationID: "conversation-a", TurnID: "turn-a", Category: "anime",
				Sources: []memory.KnowledgeIngestSource{{
					ID: "source-a", Title: "甲来源", URL: "https://a.example/item",
					Snippet: "作品甲的新篇章公开信息。", Rank: 1, FetchedAtUnixMS: 1,
				}},
			},
		},
		{
			JobID: "job-b",
			Batch: memory.KnowledgeIngestBatch{
				ID: "batch-b", ConversationID: "conversation-b", TurnID: "turn-b", Category: "game",
				Sources: []memory.KnowledgeIngestSource{{
					ID: "source-b", Title: "乙来源", URL: "https://b.example/item",
					Snippet: "作品乙续作制作决定。", Rank: 1, FetchedAtUnixMS: 2,
				}},
			},
		},
	}
	for _, claim := range claims {
		if err := engine.executeKnowledgeIngestClaim(store, claim); err != nil {
			t.Fatal(err)
		}
	}
	if len(modelPort.requests) != 2 || len(store.commits) != 2 {
		t.Fatalf("model calls=%d commits=%d, want 2/2", len(modelPort.requests), len(store.commits))
	}
	for index, request := range modelPort.requests {
		if request.Shape.Lane != model.PromptLaneKnowledgeIngest {
			t.Fatalf("request[%d] lane = %q", index, request.Shape.Lane)
		}
	}
	firstInput := fmt.Sprint(modelPort.requests[0].Input)
	secondInput := fmt.Sprint(modelPort.requests[1].Input)
	if !strings.Contains(firstInput, "source-a") || strings.Contains(firstInput, "source-b") ||
		!strings.Contains(secondInput, "source-b") || strings.Contains(secondInput, "source-a") {
		t.Fatalf("cross-batch input contamination: %#v", modelPort.requests)
	}
	if store.commits[0].batchID != "batch-a" || store.commits[0].facts[0].SourceHitIDs[0] != "source-a" ||
		store.commits[1].batchID != "batch-b" || store.commits[1].facts[0].SourceHitIDs[0] != "source-b" {
		t.Fatalf("commits = %#v", store.commits)
	}
}

func TestKnowledgeIngestRejectsUngroundedOutputWithoutFallbackOrRetry(t *testing.T) {
	modelPort := &participationModel{draft: `{"facts":[{"topic":"污染","statement":"这条事实引用了另一个批次的来源。","confidenceBasisPoints":8000,"sourceHitIDs":["source-other"]}]}`}
	service := &CompanionService{model: modelPort, cfg: participationConfig{}}
	engine := newRetentionEngine(service)
	store := &capturingKnowledgeIngestStore{}
	claim := memory.KnowledgeIngestClaim{
		JobID: "job-a",
		Batch: memory.KnowledgeIngestBatch{
			ID: "batch-a", ConversationID: "conversation-a", TurnID: "turn-a", Category: "book",
			Sources: []memory.KnowledgeIngestSource{{
				ID: "source-a", Title: "本批来源", URL: "https://a.example/book",
				Snippet: "本批次内唯一允许引用的来源。", Rank: 1, FetchedAtUnixMS: 1,
			}},
		},
	}
	if err := engine.executeKnowledgeIngestClaim(store, claim); err == nil {
		t.Fatal("executeKnowledgeIngestClaim() error = nil")
	}
	if len(modelPort.requests) != 1 || len(store.commits) != 0 || len(store.drops) != 0 {
		t.Fatalf("model calls=%d commits=%d drops=%d", len(modelPort.requests), len(store.commits), len(store.drops))
	}
}

func TestKnowledgeIngestWorkerMarksInvalidModelOutputFailed(t *testing.T) {
	modelPort := &participationModel{draft: "```json\n{\"facts\":[]}\n```"}
	service := &CompanionService{model: modelPort, cfg: participationConfig{}}
	store := &claimedKnowledgeIngestStore{
		done: make(chan struct{}),
		claim: memory.KnowledgeIngestClaim{
			JobID: "job-invalid",
			Batch: memory.KnowledgeIngestBatch{
				ID: "batch-invalid", ConversationID: "conversation-a", TurnID: "turn-a", Category: "anime",
				Sources: []memory.KnowledgeIngestSource{{
					ID: "source-a", Title: "来源", URL: "https://a.example/invalid",
					Snippet: "严格解析不接受 Markdown 包裹。", Rank: 1, FetchedAtUnixMS: 1,
				}},
			},
		},
	}
	service.memory.retention.knowledge = store
	engine := newRetentionEngine(service)
	engine.scheduleKnowledgeIngestBatches([]memory.KnowledgeIngestBatch{store.claim.Batch})
	select {
	case <-store.done:
	case <-time.After(time.Second):
		t.Fatal("invalid knowledge ingest was not failed")
	}
	engine.close()
	if store.failed != "job-invalid" || len(modelPort.requests) != 1 {
		t.Fatalf("failed=%q model calls=%d", store.failed, len(modelPort.requests))
	}
}

func TestKnowledgeIngestDropsEmptyFactBatch(t *testing.T) {
	modelPort := &participationModel{draft: `{"facts":[]}`}
	service := &CompanionService{model: modelPort, cfg: participationConfig{}}
	engine := newRetentionEngine(service)
	store := &capturingKnowledgeIngestStore{}
	claim := memory.KnowledgeIngestClaim{
		JobID: "job-empty",
		Batch: memory.KnowledgeIngestBatch{
			ID: "batch-empty", ConversationID: "conversation-a", TurnID: "turn-a", Category: "anime",
			Sources: []memory.KnowledgeIngestSource{{
				ID: "source-a", Title: "来源", URL: "https://a.example/empty",
				Snippet: "没有足够证据提取稳定事实。", Rank: 1, FetchedAtUnixMS: 1,
			}},
		},
	}
	if err := engine.executeKnowledgeIngestClaim(store, claim); err != nil {
		t.Fatal(err)
	}
	if len(modelPort.requests) != 1 || len(store.commits) != 0 || len(store.drops) != 1 || store.drops[0] != "job-empty" {
		t.Fatalf("model calls=%d commits=%d drops=%v", len(modelPort.requests), len(store.commits), store.drops)
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
	service.scheduleKnowledgeIngestBatches([]webSearchBatch{{ID: "batch", ConversationID: "conversation-1", Category: "anime", Sources: []webSearchSource{{ID: "source"}}}})
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
	engine.scheduleKnowledgeIngestBatches([]memory.KnowledgeIngestBatch{{ID: "batch", ConversationID: "conversation"}})
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
