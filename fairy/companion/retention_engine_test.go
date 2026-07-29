package companion

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fairy/memory"
	"fairy/model"
	"fairy/session"
)

type testKnowledgeDocumentFetcher struct{}

func testKnowledgeChunkID(sourceURL, text string) string {
	textSum := sha256.Sum256([]byte(text))
	chunkSum := sha256.Sum256([]byte(sourceURL + fmt.Sprintf("%x", textSum[:])))
	return fmt.Sprintf("web-chunk-%x", chunkSum[:12])
}

type selectiveKnowledgeDocumentFetcher struct{}

func (selectiveKnowledgeDocumentFetcher) FetchSource(ctx context.Context, source memory.KnowledgeIngestSource) (memory.KnowledgeDocument, error) {
	if strings.Contains(source.URL, "failed.example") {
		return memory.KnowledgeDocument{}, errors.New("source is permanently unavailable")
	}
	return (testKnowledgeDocumentFetcher{}).FetchSource(ctx, source)
}

func (testKnowledgeDocumentFetcher) FetchSource(_ context.Context, source memory.KnowledgeIngestSource) (memory.KnowledgeDocument, error) {
	text := source.Snippet
	if text == "" {
		text = source.Title
	}
	textSum := sha256.Sum256([]byte(text))
	contentHash := fmt.Sprintf("%x", textSum[:])
	return memory.KnowledgeDocument{
		SourceID: source.ID, CanonicalURL: source.URL, Title: source.Title,
		ContentHash: contentHash, ContentType: "text/plain", FetchedAtUnixMS: source.FetchedAtUnixMS,
		Chunks: []memory.KnowledgeDocumentChunk{{
			ID: testKnowledgeChunkID(source.URL, text), Ordinal: 0, Text: text, TextHash: contentHash,
		}},
	}, nil
}

type retentionKnowledgeStore struct{ calls *atomic.Int64 }

func (s retentionKnowledgeStore) EnqueueKnowledgeIngestBatches([]memory.KnowledgeIngestBatch) error {
	return nil
}

func (s retentionKnowledgeStore) ClaimKnowledgeIngestBatches(int) ([]memory.KnowledgeIngestClaim, error) {
	s.calls.Add(1)
	return nil, nil
}

func (retentionKnowledgeStore) KnowledgeDocumentsNeedExtraction(string, string, []memory.KnowledgeDocument) (bool, error) {
	return true, nil
}

func (retentionKnowledgeStore) RecallKnowledgeForIngest(memory.KnowledgeIngestFact, int) ([]memory.RetrievedKnowledge, error) {
	return nil, nil
}

func (retentionKnowledgeStore) CommitKnowledgeDocumentMutations(string, string, []memory.KnowledgeDocument, []memory.KnowledgeIngestFact, []memory.KnowledgeIngestRecall, []memory.KnowledgeIngestMutation) (int, error) {
	return 0, nil
}

func (retentionKnowledgeStore) FailKnowledgeIngestBatch(string, string) error {
	return nil
}

func (retentionKnowledgeStore) RetryKnowledgeIngestBatch(string, string, string) error {
	return nil
}

func (retentionKnowledgeStore) DropKnowledgeIngestBatch(string, string) error {
	return nil
}

type capturingKnowledgeIngestStore struct {
	commits []struct {
		jobID     string
		batchID   string
		facts     []memory.KnowledgeIngestFact
		recalls   []memory.KnowledgeIngestRecall
		mutations []memory.KnowledgeIngestMutation
	}
	drops  []string
	recall []memory.RetrievedKnowledge
}

type claimedKnowledgeIngestStore struct {
	claim   memory.KnowledgeIngestClaim
	failed  string
	done    chan struct{}
	claimed atomic.Bool
}

type isolatedKnowledgeIngestStore struct {
	capturingKnowledgeIngestStore
	claims  []memory.KnowledgeIngestClaim
	claimed bool
	failed  []string
}

func (s *isolatedKnowledgeIngestStore) ClaimKnowledgeIngestBatches(int) ([]memory.KnowledgeIngestClaim, error) {
	if s.claimed {
		return nil, nil
	}
	s.claimed = true
	return append([]memory.KnowledgeIngestClaim(nil), s.claims...), nil
}

func (s *isolatedKnowledgeIngestStore) FailKnowledgeIngestBatch(jobID, _ string) error {
	s.failed = append(s.failed, jobID)
	return nil
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

func (*claimedKnowledgeIngestStore) KnowledgeDocumentsNeedExtraction(string, string, []memory.KnowledgeDocument) (bool, error) {
	return true, nil
}

func (*claimedKnowledgeIngestStore) RecallKnowledgeForIngest(memory.KnowledgeIngestFact, int) ([]memory.RetrievedKnowledge, error) {
	return nil, nil
}

func (*claimedKnowledgeIngestStore) CommitKnowledgeDocumentMutations(string, string, []memory.KnowledgeDocument, []memory.KnowledgeIngestFact, []memory.KnowledgeIngestRecall, []memory.KnowledgeIngestMutation) (int, error) {
	return 0, errors.New("unexpected commit")
}

func (s *claimedKnowledgeIngestStore) FailKnowledgeIngestBatch(jobID, _ string) error {
	s.failed = jobID
	close(s.done)
	return nil
}

func (*claimedKnowledgeIngestStore) RetryKnowledgeIngestBatch(string, string, string) error {
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

func (s *capturingKnowledgeIngestStore) RecallKnowledgeForIngest(memory.KnowledgeIngestFact, int) ([]memory.RetrievedKnowledge, error) {
	return append([]memory.RetrievedKnowledge(nil), s.recall...), nil
}

func (s *capturingKnowledgeIngestStore) CommitKnowledgeDocumentMutations(jobID, batchID string, _ []memory.KnowledgeDocument, facts []memory.KnowledgeIngestFact, recalls []memory.KnowledgeIngestRecall, mutations []memory.KnowledgeIngestMutation) (int, error) {
	s.commits = append(s.commits, struct {
		jobID     string
		batchID   string
		facts     []memory.KnowledgeIngestFact
		recalls   []memory.KnowledgeIngestRecall
		mutations []memory.KnowledgeIngestMutation
	}{
		jobID: jobID, batchID: batchID,
		facts:     append([]memory.KnowledgeIngestFact(nil), facts...),
		recalls:   append([]memory.KnowledgeIngestRecall(nil), recalls...),
		mutations: append([]memory.KnowledgeIngestMutation(nil), mutations...),
	})
	return len(facts), nil
}

func (*capturingKnowledgeIngestStore) KnowledgeDocumentsNeedExtraction(string, string, []memory.KnowledgeDocument) (bool, error) {
	return true, nil
}

func (*capturingKnowledgeIngestStore) FailKnowledgeIngestBatch(string, string) error {
	return nil
}

func (*capturingKnowledgeIngestStore) RetryKnowledgeIngestBatch(string, string, string) error {
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

func TestRetentionEngineKnowledgeIngestWorkerWakesAndPolls(t *testing.T) {
	var calls atomic.Int64
	service := &CompanionService{}
	service.memory.retention.knowledge = retentionKnowledgeStore{calls: &calls}
	engine := newRetentionEngine(service)
	engine.start()
	engine.wakeKnowledgeIngest()
	defer engine.close()

	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() < 1 {
		t.Fatal("knowledge ingest did not run")
	}
}

func TestKnowledgeIngestExtractsAndReconcilesEachIsolatedSource(t *testing.T) {
	modelPort := &participationModel{drafts: []string{
		fmt.Sprintf(`{"facts":[{"subject":"作品甲","predicate":"新篇章公开时间","value":"十月","statement":"作品甲将在十月正式公开新篇章。","confidenceBasisPoints":8200,"evidenceChunkIDs":["%s"]}]}`, testKnowledgeChunkID("https://a.example/item", "作品甲的新篇章公开信息。")),
		`{"mutations":[{"factIndex":0,"operation":"ADD"}]}`,
		fmt.Sprintf(`{"facts":[{"subject":"作品乙","predicate":"续作状态","value":"已公布制作决定","statement":"作品乙的续作已经正式公布制作决定。","confidenceBasisPoints":8100,"evidenceChunkIDs":["%s"]}]}`, testKnowledgeChunkID("https://b.example/item", "作品乙续作制作决定。")),
		`{"mutations":[{"factIndex":0,"operation":"ADD"}]}`,
	}}
	service := &CompanionService{model: modelPort, cfg: participationConfig{}, knowledgeDocuments: testKnowledgeDocumentFetcher{}}
	engine := newRetentionEngine(service)
	store := &capturingKnowledgeIngestStore{}
	claims := []memory.KnowledgeIngestClaim{
		{
			JobID: "job-a",
			Batch: memory.KnowledgeIngestBatch{
				ID: "batch-a", ConversationID: "conversation-a", TurnID: "turn-a",
				Sources: []memory.KnowledgeIngestSource{{
					ID: "source-a", Title: "甲来源", URL: "https://a.example/item",
					Snippet: "作品甲的新篇章公开信息。", Rank: 1, FetchedAtUnixMS: 1,
				}},
			},
		},
		{
			JobID: "job-b",
			Batch: memory.KnowledgeIngestBatch{
				ID: "batch-b", ConversationID: "conversation-b", TurnID: "turn-b",
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
	if len(modelPort.requests) != 4 || len(store.commits) != 2 {
		t.Fatalf("model calls=%d commits=%d, want 4/2", len(modelPort.requests), len(store.commits))
	}
	for index, request := range modelPort.requests {
		wantLane := model.PromptLaneKnowledgeIngest
		if index%2 == 1 {
			wantLane = model.PromptLaneKnowledgeReconcile
		}
		if request.Shape.Lane != wantLane {
			t.Fatalf("request[%d] lane = %q, want %q", index, request.Shape.Lane, wantLane)
		}
	}
	firstInput := fmt.Sprint(modelPort.requests[0].Input)
	secondInput := fmt.Sprint(modelPort.requests[2].Input)
	if !strings.Contains(firstInput, "source-a") || strings.Contains(firstInput, "source-b") ||
		!strings.Contains(secondInput, "source-b") || strings.Contains(secondInput, "source-a") {
		t.Fatalf("cross-batch input contamination: %#v", modelPort.requests)
	}
	if store.commits[0].batchID != "batch-a" || store.commits[0].facts[0].EvidenceChunkIDs[0] != testKnowledgeChunkID("https://a.example/item", "作品甲的新篇章公开信息。") ||
		store.commits[1].batchID != "batch-b" || store.commits[1].facts[0].EvidenceChunkIDs[0] != testKnowledgeChunkID("https://b.example/item", "作品乙续作制作决定。") {
		t.Fatalf("commits = %#v", store.commits)
	}
	if store.commits[0].mutations[0].Operation != memory.KnowledgeMutationAdd ||
		store.commits[1].mutations[0].Operation != memory.KnowledgeMutationAdd {
		t.Fatalf("mutations = %#v, %#v", store.commits[0].mutations, store.commits[1].mutations)
	}
}

func TestKnowledgeIngestResolvesRecalledAliasBeforeCommit(t *testing.T) {
	chunkID := testKnowledgeChunkID("https://a.example/update", "作品甲的状态已从内测调整为公测。")
	modelPort := &participationModel{drafts: []string{
		fmt.Sprintf(`{"facts":[{"subject":"作品甲","predicate":"状态","value":"公测","statement":"作品甲当前已经进入公开测试阶段。","confidenceBasisPoints":8300,"evidenceChunkIDs":["%s"]}]}`, chunkID),
		`{"mutations":[{"factIndex":0,"operation":"UPDATE","memoryId":"f0m0"}]}`,
	}}
	service := &CompanionService{model: modelPort, cfg: participationConfig{}, knowledgeDocuments: testKnowledgeDocumentFetcher{}}
	engine := newRetentionEngine(service)
	store := &capturingKnowledgeIngestStore{recall: []memory.RetrievedKnowledge{{
		ID: "knowledge-old", Statement: "作品甲此前处于内部测试阶段。", ConfidenceBasisPoints: 7800,
	}}}
	claim := memory.KnowledgeIngestClaim{
		JobID: "job-update",
		Batch: memory.KnowledgeIngestBatch{
			ID: "batch-update", ConversationID: "conversation-a", TurnID: "turn-a",
			Sources: []memory.KnowledgeIngestSource{{
				ID: "source-a", Title: "状态页", URL: "https://a.example/update",
				Snippet: "作品甲的状态已从内测调整为公测。", Rank: 1, FetchedAtUnixMS: 1,
			}},
		},
	}
	if err := engine.executeKnowledgeIngestClaim(store, claim); err != nil {
		t.Fatal(err)
	}
	if len(store.commits) != 1 || store.commits[0].mutations[0].MemoryID != "knowledge-old" ||
		store.commits[0].mutations[0].Operation != memory.KnowledgeMutationUpdate {
		t.Fatalf("commits = %#v", store.commits)
	}
	reconcileInput := fmt.Sprint(modelPort.requests[1].Input)
	if strings.Contains(reconcileInput, "knowledge-old") || !strings.Contains(reconcileInput, "f0m0") {
		t.Fatalf("reconcile input leaked storage ID: %s", reconcileInput)
	}
}

func TestKnowledgeIngestSourceFailureDoesNotBlockSiblingJob(t *testing.T) {
	goodText := "作品乙已经正式公布续作制作决定。"
	modelPort := &participationModel{drafts: []string{
		fmt.Sprintf(`{"facts":[{"subject":"作品乙","predicate":"续作状态","value":"已公布","statement":"作品乙已经正式公布续作制作决定。","confidenceBasisPoints":8100,"evidenceChunkIDs":["%s"]}]}`, testKnowledgeChunkID("https://good.example/item", goodText)),
		`{"mutations":[{"factIndex":0,"operation":"ADD"}]}`,
	}}
	service := &CompanionService{model: modelPort, cfg: participationConfig{}, knowledgeDocuments: selectiveKnowledgeDocumentFetcher{}}
	engine := newRetentionEngine(service)
	store := &isolatedKnowledgeIngestStore{claims: []memory.KnowledgeIngestClaim{
		{
			JobID: "job-failed",
			Batch: memory.KnowledgeIngestBatch{
				ID: "batch-failed", ConversationID: "conversation-a", TurnID: "turn-a",
				Sources: []memory.KnowledgeIngestSource{{
					ID: "source-failed", Title: "坏来源", URL: "https://failed.example/item",
					Snippet: "无法访问的来源。", Rank: 1, FetchedAtUnixMS: 1,
				}},
			},
		},
		{
			JobID: "job-good",
			Batch: memory.KnowledgeIngestBatch{
				ID: "batch-good", ConversationID: "conversation-a", TurnID: "turn-a",
				Sources: []memory.KnowledgeIngestSource{{
					ID: "source-good", Title: "好来源", URL: "https://good.example/item",
					Snippet: goodText, Rank: 2, FetchedAtUnixMS: 1,
				}},
			},
		},
	}}
	service.memory.retention.knowledge = store
	engine.drainKnowledgeIngest()
	if !slices.Equal(store.failed, []string{"job-failed"}) || len(store.commits) != 1 || store.commits[0].jobID != "job-good" {
		t.Fatalf("failed=%v commits=%#v", store.failed, store.commits)
	}
}

func TestKnowledgeIngestRejectsUngroundedOutputWithoutFallbackOrRetry(t *testing.T) {
	modelPort := &participationModel{draft: `{"facts":[{"subject":"污染","predicate":"状态","value":"错误","statement":"这条事实引用了另一个批次的来源。","confidenceBasisPoints":8000,"evidenceChunkIDs":["chunk-other"]}]}`}
	service := &CompanionService{model: modelPort, cfg: participationConfig{}, knowledgeDocuments: testKnowledgeDocumentFetcher{}}
	engine := newRetentionEngine(service)
	store := &capturingKnowledgeIngestStore{}
	claim := memory.KnowledgeIngestClaim{
		JobID: "job-a",
		Batch: memory.KnowledgeIngestBatch{
			ID: "batch-a", ConversationID: "conversation-a", TurnID: "turn-a",
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
	service := &CompanionService{model: modelPort, cfg: participationConfig{}, knowledgeDocuments: testKnowledgeDocumentFetcher{}}
	store := &claimedKnowledgeIngestStore{
		done: make(chan struct{}),
		claim: memory.KnowledgeIngestClaim{
			JobID: "job-invalid",
			Batch: memory.KnowledgeIngestBatch{
				ID: "batch-invalid", ConversationID: "conversation-a", TurnID: "turn-a",
				Sources: []memory.KnowledgeIngestSource{{
					ID: "source-a", Title: "来源", URL: "https://a.example/invalid",
					Snippet: "严格解析不接受 Markdown 包裹。", Rank: 1, FetchedAtUnixMS: 1,
				}},
			},
		},
	}
	service.memory.retention.knowledge = store
	engine := newRetentionEngine(service)
	engine.start()
	engine.wakeKnowledgeIngest()
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
	service := &CompanionService{model: modelPort, cfg: participationConfig{}, knowledgeDocuments: testKnowledgeDocumentFetcher{}}
	engine := newRetentionEngine(service)
	store := &capturingKnowledgeIngestStore{}
	claim := memory.KnowledgeIngestClaim{
		JobID: "job-empty",
		Batch: memory.KnowledgeIngestBatch{
			ID: "batch-empty", ConversationID: "conversation-a", TurnID: "turn-a",
			Sources: []memory.KnowledgeIngestSource{{
				ID: "source-a", Title: "来源", URL: "https://a.example/empty",
				Snippet: "没有足够证据提取稳定事实。", Rank: 1, FetchedAtUnixMS: 1,
			}},
		},
	}
	if err := engine.executeKnowledgeIngestClaim(store, claim); err != nil {
		t.Fatal(err)
	}
	if len(modelPort.requests) != 1 || len(store.commits) != 1 || len(store.commits[0].facts) != 0 || len(store.drops) != 0 {
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

func TestDurableKnowledgeWakeBypassesRetentionSlotsWhileDesktopRemainsBounded(t *testing.T) {
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
	service.retention.wakeKnowledgeIngest()
	service.backgroundErrorMu.Lock()
	backgroundErr := service.backgroundError
	service.backgroundErrorMu.Unlock()
	if backgroundErr != nil {
		t.Fatalf("durable wake background error = %v", backgroundErr)
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
	engine.wakeKnowledgeIngest()
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
