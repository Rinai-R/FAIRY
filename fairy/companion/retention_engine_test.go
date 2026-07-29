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

	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/persona"
	"fairy/session"
)

type testKnowledgeDocumentFetcher struct{}

func testKnowledgeChunkID(sourceURL, text string) string {
	textSum := sha256.Sum256([]byte(text))
	chunkSum := sha256.Sum256([]byte(sourceURL + fmt.Sprintf("%x", textSum[:])))
	return fmt.Sprintf("web-evidence-%x", chunkSum[:12])
}

type selectiveKnowledgeDocumentFetcher struct{}

type oversizedKnowledgeDocumentFetcher struct{}

func (oversizedKnowledgeDocumentFetcher) FetchSource(_ context.Context, source memory.KnowledgeIngestSource) (memory.KnowledgeDocument, error) {
	content := strings.Repeat("完整正文不能被静默截断。", 4000)
	sum := sha256.Sum256([]byte(content))
	return memory.KnowledgeDocument{
		SourceID: source.ID, CanonicalURL: source.URL, Title: source.Title,
		Content: content, ContentHash: fmt.Sprintf("%x", sum[:]),
		EvidenceID: "web-evidence-oversized", ContentType: "text/plain",
	}, nil
}

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
		Content: text, ContentHash: contentHash,
		EvidenceID:  testKnowledgeChunkID(source.URL, text),
		ContentType: "text/plain", FetchedAtUnixMS: source.FetchedAtUnixMS,
	}, nil
}

type retentionKnowledgeStore struct {
	calls      *atomic.Int64
	claimLimit *atomic.Int64
}

func (s retentionKnowledgeStore) EnqueueKnowledgeIngestTasks([]memory.KnowledgeIngestTask) error {
	return nil
}

func (s retentionKnowledgeStore) ClaimKnowledgeIngestTasksContext(_ context.Context, limit int) ([]memory.KnowledgeIngestClaim, error) {
	s.calls.Add(1)
	if s.claimLimit != nil {
		s.claimLimit.Store(int64(limit))
	}
	return nil, nil
}

func (retentionKnowledgeStore) KnowledgeIngestLeaseDuration() time.Duration {
	return time.Minute
}

func (retentionKnowledgeStore) RenewKnowledgeIngestLeaseContext(context.Context, string) error {
	return nil
}

func (retentionKnowledgeStore) ReleaseClaimedKnowledgeIngestJob(string) error {
	return nil
}

func (retentionKnowledgeStore) KnowledgeDocumentNeedsExtractionContext(context.Context, string, string, memory.KnowledgeDocument) (bool, error) {
	return true, nil
}

func (retentionKnowledgeStore) SearchKnowledgeForIngestContext(context.Context, string, int) ([]memory.RetrievedKnowledge, error) {
	return nil, nil
}

func (retentionKnowledgeStore) CommitKnowledgeDocumentActionsContext(context.Context, string, string, memory.KnowledgeDocument, []string, []memory.KnowledgeDocumentAction) (int, error) {
	return 0, nil
}

func (retentionKnowledgeStore) FailClaimedKnowledgeIngestJob(string, string) error {
	return nil
}

func (retentionKnowledgeStore) RetryClaimedKnowledgeIngestJob(string, string, string) error {
	return nil
}

func (retentionKnowledgeStore) DropClaimedKnowledgeIngestJob(string, string) error {
	return nil
}

type capturingKnowledgeIngestStore struct {
	preflightDocuments []memory.KnowledgeDocument
	commits            []struct {
		jobID    string
		batchID  string
		document memory.KnowledgeDocument
		supplied []string
		actions  []memory.KnowledgeDocumentAction
	}
	drops  []string
	recall []memory.RetrievedKnowledge
}

type scriptedKnowledgeAgentModel struct {
	responses [][]model.StreamEvent
	requests  []model.CompiledPromptRequest
}

type renewalBlockingKnowledgeAgentModel struct {
	renewed <-chan struct{}
}

type renewalStartedInvalidKnowledgeAgentModel struct {
	renewalStarted <-chan struct{}
}

type shutdownBlockingKnowledgeAgentModel struct {
	started chan struct{}
}

func (*renewalBlockingKnowledgeAgentModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, errors.New("unexpected legacy prompt execution")
}

func (m *renewalBlockingKnowledgeAgentModel) ExecuteRequestContext(ctx context.Context, _ model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.renewed:
		return []model.StreamEvent{{Type: "text_delta", Data: `{"actions":[]}`}}, nil
	}
}

func (*renewalStartedInvalidKnowledgeAgentModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, errors.New("unexpected legacy prompt execution")
}

func (m *renewalStartedInvalidKnowledgeAgentModel) ExecuteRequestContext(ctx context.Context, _ model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.renewalStarted:
		return []model.StreamEvent{{Type: "text_delta", Data: "invalid-json"}}, nil
	}
}

func (*shutdownBlockingKnowledgeAgentModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, errors.New("unexpected legacy prompt execution")
}

func (m *shutdownBlockingKnowledgeAgentModel) ExecuteRequestContext(ctx context.Context, _ model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	close(m.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

type smallKnowledgeContextConfig struct {
	participationConfig
	contextWindowTokens uint64
}

func (c smallKnowledgeContextConfig) ModelConnection() (config.ModelConnection, error) {
	connection, err := c.participationConfig.ModelConnection()
	connection.ContextWindowTokens = c.contextWindowTokens
	return connection, err
}

func (*scriptedKnowledgeAgentModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, errors.New("unexpected legacy prompt execution")
}

func (m *scriptedKnowledgeAgentModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	m.requests = append(m.requests, request)
	index := len(m.requests) - 1
	if index >= len(m.responses) {
		return nil, errors.New("unexpected knowledge agent model request")
	}
	return append([]model.StreamEvent(nil), m.responses[index]...), nil
}

type claimedKnowledgeIngestStore struct {
	claim    memory.KnowledgeIngestClaim
	failed   string
	retried  string
	released string
	done     chan struct{}
	claimed  atomic.Bool
}

type isolatedKnowledgeIngestStore struct {
	capturingKnowledgeIngestStore
	claims  []memory.KnowledgeIngestClaim
	claimed bool
	failed  []string
}

type renewingKnowledgeIngestStore struct {
	capturingKnowledgeIngestStore
	duration time.Duration
	renewed  chan struct{}
	renewals atomic.Int64
	renewErr error
}

type blockingKnowledgeSearchStore struct {
	capturingKnowledgeIngestStore
	started chan struct{}
	release chan struct{}
}

type blockingKnowledgeClaimStore struct {
	retentionKnowledgeStore
	started chan struct{}
}

type cancellationBlockingRenewalStore struct {
	capturingKnowledgeIngestStore
	started chan struct{}
}

func (*cancellationBlockingRenewalStore) KnowledgeIngestLeaseDuration() time.Duration {
	return 3 * time.Millisecond
}

func (s *cancellationBlockingRenewalStore) RenewKnowledgeIngestLeaseContext(ctx context.Context, _ string) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}

func (s *blockingKnowledgeClaimStore) ClaimKnowledgeIngestTasksContext(ctx context.Context, _ int) ([]memory.KnowledgeIngestClaim, error) {
	close(s.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *blockingKnowledgeSearchStore) SearchKnowledgeForIngestContext(ctx context.Context, _ string, _ int) ([]memory.RetrievedKnowledge, error) {
	close(s.started)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return nil, nil
	}
}

func (s *renewingKnowledgeIngestStore) KnowledgeIngestLeaseDuration() time.Duration {
	return s.duration
}

func (s *renewingKnowledgeIngestStore) RenewKnowledgeIngestLeaseContext(context.Context, string) error {
	if s.renewErr != nil {
		s.renewals.Add(1)
		return s.renewErr
	}
	if s.renewals.Add(1) == 1 {
		close(s.renewed)
	}
	return nil
}

func (s *isolatedKnowledgeIngestStore) ClaimKnowledgeIngestTasksContext(context.Context, int) ([]memory.KnowledgeIngestClaim, error) {
	if s.claimed {
		return nil, nil
	}
	s.claimed = true
	return append([]memory.KnowledgeIngestClaim(nil), s.claims...), nil
}

func (s *isolatedKnowledgeIngestStore) FailClaimedKnowledgeIngestJob(jobID, _ string) error {
	s.failed = append(s.failed, jobID)
	return nil
}

func (*isolatedKnowledgeIngestStore) ReleaseClaimedKnowledgeIngestJob(string) error {
	return nil
}

func (*claimedKnowledgeIngestStore) EnqueueKnowledgeIngestTasks([]memory.KnowledgeIngestTask) error {
	return nil
}

func (s *claimedKnowledgeIngestStore) ClaimKnowledgeIngestTasksContext(context.Context, int) ([]memory.KnowledgeIngestClaim, error) {
	if !s.claimed.CompareAndSwap(false, true) {
		return nil, nil
	}
	return []memory.KnowledgeIngestClaim{s.claim}, nil
}

func (*claimedKnowledgeIngestStore) KnowledgeIngestLeaseDuration() time.Duration {
	return time.Minute
}

func (*claimedKnowledgeIngestStore) RenewKnowledgeIngestLeaseContext(context.Context, string) error {
	return nil
}

func (s *claimedKnowledgeIngestStore) ReleaseClaimedKnowledgeIngestJob(jobID string) error {
	s.released = jobID
	return nil
}

func (*claimedKnowledgeIngestStore) KnowledgeDocumentNeedsExtractionContext(context.Context, string, string, memory.KnowledgeDocument) (bool, error) {
	return true, nil
}

func (*claimedKnowledgeIngestStore) SearchKnowledgeForIngestContext(context.Context, string, int) ([]memory.RetrievedKnowledge, error) {
	return nil, nil
}

func (*claimedKnowledgeIngestStore) CommitKnowledgeDocumentActionsContext(context.Context, string, string, memory.KnowledgeDocument, []string, []memory.KnowledgeDocumentAction) (int, error) {
	return 0, errors.New("unexpected commit")
}

func (s *claimedKnowledgeIngestStore) FailClaimedKnowledgeIngestJob(jobID, _ string) error {
	s.failed = jobID
	close(s.done)
	return nil
}

func (s *claimedKnowledgeIngestStore) RetryClaimedKnowledgeIngestJob(jobID, _, _ string) error {
	s.retried = jobID
	close(s.done)
	return nil
}

func (*claimedKnowledgeIngestStore) DropClaimedKnowledgeIngestJob(string, string) error {
	return errors.New("unexpected drop")
}

func (*capturingKnowledgeIngestStore) EnqueueKnowledgeIngestTasks([]memory.KnowledgeIngestTask) error {
	return nil
}

func (*capturingKnowledgeIngestStore) ClaimKnowledgeIngestTasksContext(context.Context, int) ([]memory.KnowledgeIngestClaim, error) {
	return nil, nil
}

func (*capturingKnowledgeIngestStore) KnowledgeIngestLeaseDuration() time.Duration {
	return time.Minute
}

func (*capturingKnowledgeIngestStore) RenewKnowledgeIngestLeaseContext(context.Context, string) error {
	return nil
}

func (*capturingKnowledgeIngestStore) ReleaseClaimedKnowledgeIngestJob(string) error {
	return nil
}

func (s *capturingKnowledgeIngestStore) SearchKnowledgeForIngestContext(context.Context, string, int) ([]memory.RetrievedKnowledge, error) {
	return append([]memory.RetrievedKnowledge(nil), s.recall...), nil
}

func (s *capturingKnowledgeIngestStore) CommitKnowledgeDocumentActionsContext(_ context.Context, jobID, batchID string, document memory.KnowledgeDocument, supplied []string, actions []memory.KnowledgeDocumentAction) (int, error) {
	s.commits = append(s.commits, struct {
		jobID    string
		batchID  string
		document memory.KnowledgeDocument
		supplied []string
		actions  []memory.KnowledgeDocumentAction
	}{
		jobID: jobID, batchID: batchID,
		document: document,
		supplied: append([]string(nil), supplied...),
		actions:  append([]memory.KnowledgeDocumentAction(nil), actions...),
	})
	return len(actions), nil
}

func (s *capturingKnowledgeIngestStore) KnowledgeDocumentNeedsExtractionContext(_ context.Context, _ string, _ string, document memory.KnowledgeDocument) (bool, error) {
	s.preflightDocuments = append(s.preflightDocuments, document)
	return true, nil
}

func (*capturingKnowledgeIngestStore) FailClaimedKnowledgeIngestJob(string, string) error {
	return nil
}

func (*capturingKnowledgeIngestStore) RetryClaimedKnowledgeIngestJob(string, string, string) error {
	return nil
}

func (s *capturingKnowledgeIngestStore) DropClaimedKnowledgeIngestJob(jobID, _ string) error {
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
	var claimLimit atomic.Int64
	service := &CompanionService{}
	store := retentionKnowledgeStore{calls: &calls, claimLimit: &claimLimit}
	service.memory.retention.knowledge = store
	service.memory.retention.knowledgeLease = store
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
	if claimLimit.Load() != 1 {
		t.Fatalf("knowledge ingest claim limit = %d, want 1 for serial execution", claimLimit.Load())
	}
}

func TestRetentionEngineCloseCancelsBlockedKnowledgeClaim(t *testing.T) {
	service := &CompanionService{}
	store := &blockingKnowledgeClaimStore{
		retentionKnowledgeStore: retentionKnowledgeStore{calls: &atomic.Int64{}},
		started:                 make(chan struct{}),
	}
	service.memory.retention.knowledge = store
	service.memory.retention.knowledgeLease = store
	engine := newRetentionEngine(service)
	engine.start()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("knowledge claim did not start")
	}
	closed := make(chan struct{})
	go func() {
		engine.close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("retention close did not cancel blocked knowledge claim")
	}
	service.backgroundErrorMu.Lock()
	backgroundErr := service.backgroundError
	service.backgroundErrorMu.Unlock()
	if backgroundErr != nil {
		t.Fatalf("worker shutdown recorded background error: %v", backgroundErr)
	}
}

func TestKnowledgeIngestExtractsAndReconcilesEachIsolatedSource(t *testing.T) {
	modelPort := &participationModel{drafts: []string{
		`{"actions":[{"operation":"ADD","content":"作品甲已经正式公开新篇章的相关信息。","confidenceBasisPoints":8200,"evidence":"作品甲的新篇章公开信息。"}]}`,
		`{"actions":[{"operation":"ADD","content":"作品乙已经正式公布续作制作决定。","confidenceBasisPoints":8100,"evidence":"作品乙续作制作决定。"}]}`,
	}}
	service := &CompanionService{model: modelPort, cfg: participationConfig{}, knowledgeDocuments: testKnowledgeDocumentFetcher{}}
	engine := newRetentionEngine(service)
	store := &capturingKnowledgeIngestStore{}
	claims := []memory.KnowledgeIngestClaim{
		{
			JobID: "job-a",
			Task: memory.KnowledgeIngestTask{
				ID: "batch-a", ConversationID: "conversation-a", TurnID: "turn-a",
				Source: memory.KnowledgeIngestSource{
					ID: "source-a", Title: "甲来源", URL: "https://a.example/item",
					Snippet: "作品甲的新篇章公开信息。", Rank: 1, FetchedAtUnixMS: 1,
				},
			},
		},
		{
			JobID: "job-b",
			Task: memory.KnowledgeIngestTask{
				ID: "batch-b", ConversationID: "conversation-b", TurnID: "turn-b",
				Source: memory.KnowledgeIngestSource{
					ID: "source-b", Title: "乙来源", URL: "https://b.example/item",
					Snippet: "作品乙续作制作决定。", Rank: 1, FetchedAtUnixMS: 2,
				},
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
	if len(store.preflightDocuments) != 2 {
		t.Fatalf("preflight documents = %#v", store.preflightDocuments)
	}
	for index, request := range modelPort.requests {
		if request.Shape.Lane != model.PromptLaneKnowledgeReconcile || len(request.Tools) != 1 || request.Tools[0].Name != knowledgeSearchToolName {
			t.Fatalf("request[%d] = %#v", index, request)
		}
	}
	firstInput := fmt.Sprint(modelPort.requests[0].Input)
	secondInput := fmt.Sprint(modelPort.requests[1].Input)
	if !strings.Contains(firstInput, "source-a") || strings.Contains(firstInput, "source-b") ||
		!strings.Contains(secondInput, "source-b") || strings.Contains(secondInput, "source-a") {
		t.Fatalf("cross-batch input contamination: %#v", modelPort.requests)
	}
	if store.commits[0].batchID != "batch-a" || store.commits[0].document.EvidenceID != testKnowledgeChunkID("https://a.example/item", "作品甲的新篇章公开信息。") ||
		store.commits[1].batchID != "batch-b" || store.commits[1].document.EvidenceID != testKnowledgeChunkID("https://b.example/item", "作品乙续作制作决定。") {
		t.Fatalf("commits = %#v", store.commits)
	}
	wantRevision := knowledgeReconcilerRevision(
		persona.KnowledgeReconcileInstructions,
		knowledgeAgentContractRevision,
	)
	for index := range store.commits {
		if store.preflightDocuments[index].ReconcilerRevision != wantRevision ||
			store.commits[index].document.ReconcilerRevision != wantRevision {
			t.Fatalf(
				"revision[%d] preflight=%q commit=%q want=%q",
				index,
				store.preflightDocuments[index].ReconcilerRevision,
				store.commits[index].document.ReconcilerRevision,
				wantRevision,
			)
		}
	}
	if store.commits[0].actions[0].Operation != memory.KnowledgeMutationAdd ||
		store.commits[1].actions[0].Operation != memory.KnowledgeMutationAdd {
		t.Fatalf("actions = %#v, %#v", store.commits[0].actions, store.commits[1].actions)
	}
}

func TestKnowledgeIngestResolvesRecalledAliasBeforeCommit(t *testing.T) {
	modelPort := &scriptedKnowledgeAgentModel{responses: [][]model.StreamEvent{
		{{Type: "function_calls", FunctionCalls: []model.FunctionCall{{
			CallID: "search-1", Name: knowledgeSearchToolName,
			Arguments: `{"query":"作品甲当前公开测试阶段"}`,
		}}}},
		{{Type: "text_delta", Data: `{"actions":[{"operation":"UPDATE","memoryId":"k0","content":"作品甲当前已经进入公开测试阶段，之前的内部测试状态已经失效。","confidenceBasisPoints":8300,"evidence":"作品甲的状态已从内测调整为公测。"}]}`}},
	}}
	service := &CompanionService{model: modelPort, cfg: participationConfig{}, knowledgeDocuments: testKnowledgeDocumentFetcher{}}
	engine := newRetentionEngine(service)
	store := &capturingKnowledgeIngestStore{recall: []memory.RetrievedKnowledge{{
		ID: "knowledge-old", Statement: "作品甲此前处于内部测试阶段。", ConfidenceBasisPoints: 7800,
	}}}
	claim := memory.KnowledgeIngestClaim{
		JobID: "job-update",
		Task: memory.KnowledgeIngestTask{
			ID: "batch-update", ConversationID: "conversation-a", TurnID: "turn-a",
			Source: memory.KnowledgeIngestSource{
				ID: "source-a", Title: "状态页", URL: "https://a.example/update",
				Snippet: "作品甲的状态已从内测调整为公测。", Rank: 1, FetchedAtUnixMS: 1,
			},
		},
	}
	if err := engine.executeKnowledgeIngestClaim(store, claim); err != nil {
		t.Fatal(err)
	}
	if len(store.commits) != 1 || store.commits[0].actions[0].MemoryID != "knowledge-old" ||
		store.commits[0].actions[0].Operation != memory.KnowledgeMutationUpdate ||
		!slices.Equal(store.commits[0].supplied, []string{"knowledge-old"}) {
		t.Fatalf("commits = %#v", store.commits)
	}
	reconcileInput := fmt.Sprint(modelPort.requests[1].Input)
	var toolResult string
	for _, item := range modelPort.requests[1].Input {
		if item.Type == model.PromptItemToolResult && item.Parts != nil {
			for _, part := range *item.Parts {
				toolResult += part.Text
			}
		}
	}
	if strings.Contains(reconcileInput+toolResult, "knowledge-old") || !strings.Contains(toolResult, `"id":"k0"`) {
		t.Fatalf("reconcile input=%s toolResult=%s", reconcileInput, toolResult)
	}
}

func TestKnowledgeIngestSourceFailureDoesNotBlockSiblingJob(t *testing.T) {
	goodText := "作品乙已经正式公布续作制作决定。"
	modelPort := &participationModel{drafts: []string{
		`{"actions":[{"operation":"ADD","content":"作品乙已经正式公布续作制作决定。","confidenceBasisPoints":8100,"evidence":"作品乙已经正式公布续作制作决定。"}]}`,
	}}
	service := &CompanionService{model: modelPort, cfg: participationConfig{}, knowledgeDocuments: selectiveKnowledgeDocumentFetcher{}}
	engine := newRetentionEngine(service)
	store := &isolatedKnowledgeIngestStore{claims: []memory.KnowledgeIngestClaim{
		{
			JobID: "job-failed",
			Task: memory.KnowledgeIngestTask{
				ID: "batch-failed", ConversationID: "conversation-a", TurnID: "turn-a",
				Source: memory.KnowledgeIngestSource{
					ID: "source-failed", Title: "坏来源", URL: "https://failed.example/item",
					Snippet: "无法访问的来源。", Rank: 1, FetchedAtUnixMS: 1,
				},
			},
		},
		{
			JobID: "job-good",
			Task: memory.KnowledgeIngestTask{
				ID: "batch-good", ConversationID: "conversation-a", TurnID: "turn-a",
				Source: memory.KnowledgeIngestSource{
					ID: "source-good", Title: "好来源", URL: "https://good.example/item",
					Snippet: goodText, Rank: 2, FetchedAtUnixMS: 1,
				},
			},
		},
	}}
	service.memory.retention.knowledge = store
	service.memory.retention.knowledgeLease = store
	engine.drainKnowledgeIngest()
	if !slices.Equal(store.failed, []string{"job-failed"}) || len(store.commits) != 1 || store.commits[0].jobID != "job-good" {
		t.Fatalf("failed=%v commits=%#v", store.failed, store.commits)
	}
}

func TestKnowledgeIngestRejectsUngroundedOutputWithoutFallbackOrRetry(t *testing.T) {
	modelPort := &participationModel{draft: `{"actions":[{"operation":"ADD","content":"这条知识引用了另一个批次的来源。","confidenceBasisPoints":8000,"evidence":"另一个批次才有的证据正文"}]}`}
	service := &CompanionService{model: modelPort, cfg: participationConfig{}, knowledgeDocuments: testKnowledgeDocumentFetcher{}}
	engine := newRetentionEngine(service)
	store := &capturingKnowledgeIngestStore{}
	claim := memory.KnowledgeIngestClaim{
		JobID: "job-a",
		Task: memory.KnowledgeIngestTask{
			ID: "batch-a", ConversationID: "conversation-a", TurnID: "turn-a",
			Source: memory.KnowledgeIngestSource{
				ID: "source-a", Title: "本批来源", URL: "https://a.example/book",
				Snippet: "本批次内唯一允许引用的来源。", Rank: 1, FetchedAtUnixMS: 1,
			},
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
	modelPort := &participationModel{draft: "```json\n{\"actions\":[]}\n```"}
	service := &CompanionService{model: modelPort, cfg: participationConfig{}, knowledgeDocuments: testKnowledgeDocumentFetcher{}}
	store := &claimedKnowledgeIngestStore{
		done: make(chan struct{}),
		claim: memory.KnowledgeIngestClaim{
			JobID: "job-invalid",
			Task: memory.KnowledgeIngestTask{
				ID: "batch-invalid", ConversationID: "conversation-a", TurnID: "turn-a",
				Source: memory.KnowledgeIngestSource{
					ID: "source-a", Title: "来源", URL: "https://a.example/invalid",
					Snippet: "严格解析不接受 Markdown 包裹。", Rank: 1, FetchedAtUnixMS: 1,
				},
			},
		},
	}
	service.memory.retention.knowledge = store
	service.memory.retention.knowledgeLease = store
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

func TestKnowledgeIngestWorkerRenewsLeaseDuringModelCall(t *testing.T) {
	store := &renewingKnowledgeIngestStore{
		duration: 30 * time.Millisecond,
		renewed:  make(chan struct{}),
	}
	service := &CompanionService{
		model:              &renewalBlockingKnowledgeAgentModel{renewed: store.renewed},
		cfg:                participationConfig{},
		knowledgeDocuments: testKnowledgeDocumentFetcher{},
	}
	engine := newRetentionEngine(service)
	defer engine.close()
	claim := memory.KnowledgeIngestClaim{
		JobID: "job-renew",
		Task: memory.KnowledgeIngestTask{
			ID: "batch-renew", ConversationID: "conversation-renew", TurnID: "turn-renew",
			Source: memory.KnowledgeIngestSource{
				ID: "source-renew", Title: "续租来源", URL: "https://renew.example/item",
				Snippet: "完整文档模型调用期间必须持续续租。", Rank: 1, FetchedAtUnixMS: 1,
			},
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- engine.runKnowledgeIngestClaim(store, store, claim)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("knowledge ingest lease renewal did not unblock the model call")
	}
	if store.renewals.Load() < 1 || len(store.commits) != 1 {
		t.Fatalf("renewals=%d commits=%d", store.renewals.Load(), len(store.commits))
	}
}

func TestKnowledgeIngestWorkerCancelsModelWhenLeaseRenewalFails(t *testing.T) {
	store := &renewingKnowledgeIngestStore{
		duration: 30 * time.Millisecond,
		renewed:  make(chan struct{}),
		renewErr: errors.New("lease owner changed"),
	}
	service := &CompanionService{
		model:              &renewalBlockingKnowledgeAgentModel{},
		cfg:                participationConfig{},
		knowledgeDocuments: testKnowledgeDocumentFetcher{},
	}
	engine := newRetentionEngine(service)
	defer engine.close()
	claim := memory.KnowledgeIngestClaim{
		JobID: "job-renew-failed",
		Task: memory.KnowledgeIngestTask{
			ID: "batch-renew-failed", ConversationID: "conversation-renew", TurnID: "turn-renew",
			Source: memory.KnowledgeIngestSource{
				ID: "source-renew", Title: "续租失败来源", URL: "https://renew.example/failed",
				Snippet: "续租失败必须取消当前模型请求。", Rank: 1, FetchedAtUnixMS: 1,
			},
		},
	}
	err := engine.runKnowledgeIngestClaim(store, store, claim)
	var transient transientKnowledgeIngestError
	if !errors.As(err, &transient) || transient.category != "lease_renewal" {
		t.Fatalf("renewal error = %v", err)
	}
	if store.renewals.Load() != 1 || len(store.commits) != 0 {
		t.Fatalf("renewals=%d commits=%d", store.renewals.Load(), len(store.commits))
	}
}

func TestKnowledgeIngestWorkerCancelsBlockedStoreSearchOnClose(t *testing.T) {
	modelPort := &scriptedKnowledgeAgentModel{responses: [][]model.StreamEvent{{
		{Type: "function_calls", FunctionCalls: []model.FunctionCall{{
			CallID: "search-blocked", Name: knowledgeSearchToolName,
			Arguments: `{"query":"作品当前公开状态信息"}`,
		}}},
	}}}
	store := &blockingKnowledgeSearchStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(store.release)
	service := &CompanionService{
		model:              modelPort,
		cfg:                participationConfig{},
		knowledgeDocuments: testKnowledgeDocumentFetcher{},
	}
	engine := newRetentionEngine(service)
	claim := memory.KnowledgeIngestClaim{
		JobID: "job-blocked-search",
		Task: memory.KnowledgeIngestTask{
			ID: "batch-blocked-search", ConversationID: "conversation-a", TurnID: "turn-a",
			Source: memory.KnowledgeIngestSource{
				ID: "source-a", Title: "来源", URL: "https://a.example/blocked-search",
				Snippet: "作品当前公开状态信息。", Rank: 1, FetchedAtUnixMS: 1,
			},
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- engine.runKnowledgeIngestClaim(store, store, claim)
	}()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("knowledge search did not start")
	}
	engine.workerCancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked search cancellation error = %v", err)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("worker cancellation did not cancel the blocked knowledge search")
	}
	if len(store.commits) != 0 {
		t.Fatalf("blocked search cancellation committed %d batches", len(store.commits))
	}
}

func TestKnowledgeIngestWorkerDoesNotMaskTerminalErrorWithRenewalCancellation(t *testing.T) {
	store := &cancellationBlockingRenewalStore{started: make(chan struct{})}
	service := &CompanionService{
		model:              &renewalStartedInvalidKnowledgeAgentModel{renewalStarted: store.started},
		cfg:                participationConfig{},
		knowledgeDocuments: testKnowledgeDocumentFetcher{},
	}
	engine := newRetentionEngine(service)
	defer engine.close()
	claim := memory.KnowledgeIngestClaim{
		JobID: "job-terminal-during-renewal",
		Task: memory.KnowledgeIngestTask{
			ID: "batch-terminal-during-renewal", ConversationID: "conversation-a", TurnID: "turn-a",
			Source: memory.KnowledgeIngestSource{
				ID: "source-a", Title: "来源", URL: "https://a.example/terminal-during-renewal",
				Snippet: "模型将返回明确的非法结构。", Rank: 1, FetchedAtUnixMS: 1,
			},
		},
	}
	err := engine.runKnowledgeIngestClaim(store, store, claim)
	var transient transientKnowledgeIngestError
	if errors.As(err, &transient) {
		t.Fatalf("terminal model output was masked as transient %q: %v", transient.category, err)
	}
	if err == nil || !strings.Contains(err.Error(), "strict JSON") {
		t.Fatalf("terminal model output error = %v", err)
	}
	if len(store.commits) != 0 {
		t.Fatalf("terminal model output committed %d batches", len(store.commits))
	}
}

func TestRetentionEngineCloseDoesNotClassifyCanceledKnowledgeClaimAsProviderFailure(t *testing.T) {
	modelPort := &shutdownBlockingKnowledgeAgentModel{started: make(chan struct{})}
	store := &claimedKnowledgeIngestStore{
		claim: memory.KnowledgeIngestClaim{
			JobID: "job-shutdown",
			Task: memory.KnowledgeIngestTask{
				ID: "batch-shutdown", ConversationID: "conversation-shutdown", TurnID: "turn-shutdown",
				Source: memory.KnowledgeIngestSource{
					ID: "source-shutdown", Title: "关闭中的来源", URL: "https://shutdown.example/item",
					Snippet: "Core 关闭时当前知识任务应该保持可恢复。", Rank: 1, FetchedAtUnixMS: 1,
				},
			},
		},
		done: make(chan struct{}),
	}
	service := &CompanionService{
		model:              modelPort,
		cfg:                participationConfig{},
		knowledgeDocuments: testKnowledgeDocumentFetcher{},
	}
	service.memory.retention.knowledge = store
	service.memory.retention.knowledgeLease = store
	engine := newRetentionEngine(service)
	engine.start()
	engine.wakeKnowledgeIngest()
	select {
	case <-modelPort.started:
	case <-time.After(time.Second):
		t.Fatal("knowledge model call did not start")
	}
	engine.close()
	if store.failed != "" || store.retried != "" || store.released != store.claim.JobID {
		t.Fatalf(
			"shutdown cancellation settled as failed=%q retried=%q released=%q",
			store.failed, store.retried, store.released,
		)
	}
}

func TestKnowledgeIngestWorkerStillRetriesProviderFailureWhileRunning(t *testing.T) {
	store := &claimedKnowledgeIngestStore{
		claim: memory.KnowledgeIngestClaim{
			JobID: "job-provider-failure",
			Task: memory.KnowledgeIngestTask{
				ID: "batch-provider-failure", ConversationID: "conversation-provider", TurnID: "turn-provider",
				Source: memory.KnowledgeIngestSource{
					ID: "source-provider", Title: "提供方失败来源", URL: "https://provider.example/item",
					Snippet: "Worker 仍运行时真实提供方错误必须继续重试。", Rank: 1, FetchedAtUnixMS: 1,
				},
			},
		},
		done: make(chan struct{}),
	}
	service := &CompanionService{
		model:              &participationModel{err: errors.New("provider temporarily unavailable")},
		cfg:                participationConfig{},
		knowledgeDocuments: testKnowledgeDocumentFetcher{},
	}
	service.memory.retention.knowledge = store
	service.memory.retention.knowledgeLease = store
	engine := newRetentionEngine(service)
	engine.start()
	engine.wakeKnowledgeIngest()
	select {
	case <-store.done:
	case <-time.After(time.Second):
		t.Fatal("provider failure was not settled")
	}
	engine.close()
	if store.retried != store.claim.JobID || store.failed != "" || store.released != "" {
		t.Fatalf(
			"provider failure settled as failed=%q retried=%q released=%q",
			store.failed, store.retried, store.released,
		)
	}
}

func TestKnowledgeIngestCommitsEmptyActionDocument(t *testing.T) {
	modelPort := &participationModel{draft: `{"actions":[]}`}
	service := &CompanionService{model: modelPort, cfg: participationConfig{}, knowledgeDocuments: testKnowledgeDocumentFetcher{}}
	engine := newRetentionEngine(service)
	store := &capturingKnowledgeIngestStore{}
	claim := memory.KnowledgeIngestClaim{
		JobID: "job-empty",
		Task: memory.KnowledgeIngestTask{
			ID: "batch-empty", ConversationID: "conversation-a", TurnID: "turn-a",
			Source: memory.KnowledgeIngestSource{
				ID: "source-a", Title: "来源", URL: "https://a.example/empty",
				Snippet: "没有足够证据提取稳定事实。", Rank: 1, FetchedAtUnixMS: 1,
			},
		},
	}
	if err := engine.executeKnowledgeIngestClaim(store, claim); err != nil {
		t.Fatal(err)
	}
	if len(modelPort.requests) != 1 || len(store.commits) != 1 || len(store.commits[0].actions) != 0 || len(store.drops) != 0 {
		t.Fatalf("model calls=%d commits=%d drops=%v", len(modelPort.requests), len(store.commits), store.drops)
	}
}

func TestKnowledgeAgentRejectsOversizedCompleteDocumentBeforeModelCall(t *testing.T) {
	modelPort := &participationModel{draft: `{"actions":[]}`}
	service := &CompanionService{
		model: modelPort,
		cfg: smallKnowledgeContextConfig{
			participationConfig: participationConfig{}, contextWindowTokens: 4096,
		},
		knowledgeDocuments: oversizedKnowledgeDocumentFetcher{},
	}
	engine := newRetentionEngine(service)
	store := &capturingKnowledgeIngestStore{}
	claim := memory.KnowledgeIngestClaim{
		JobID: "job-oversized",
		Task: memory.KnowledgeIngestTask{
			ID: "batch-oversized", ConversationID: "conversation-a", TurnID: "turn-a",
			Source: memory.KnowledgeIngestSource{
				ID: "source-a", Title: "超长来源", URL: "https://a.example/oversized",
				Snippet: "超长正文来源。", Rank: 1, FetchedAtUnixMS: 1,
			},
		},
	}
	if err := engine.executeKnowledgeIngestClaim(store, claim); err == nil {
		t.Fatal("executeKnowledgeIngestClaim() error = nil")
	}
	if len(modelPort.requests) != 0 || len(store.commits) != 0 {
		t.Fatalf("model calls=%d commits=%d", len(modelPort.requests), len(store.commits))
	}
}

func TestKnowledgeAgentRejectsToolCallsBeyondBudget(t *testing.T) {
	calls := make([]model.FunctionCall, maxKnowledgeAgentToolCalls+1)
	for index := range calls {
		calls[index] = model.FunctionCall{
			CallID: fmt.Sprintf("search-%d", index), Name: knowledgeSearchToolName,
			Arguments: `{"query":"作品当前公开状态信息"}`,
		}
	}
	modelPort := &scriptedKnowledgeAgentModel{responses: [][]model.StreamEvent{{
		{Type: "function_calls", FunctionCalls: calls},
	}}}
	service := &CompanionService{
		model: modelPort, cfg: participationConfig{},
		knowledgeDocuments: testKnowledgeDocumentFetcher{},
	}
	engine := newRetentionEngine(service)
	store := &capturingKnowledgeIngestStore{}
	claim := memory.KnowledgeIngestClaim{
		JobID: "job-tool-budget",
		Task: memory.KnowledgeIngestTask{
			ID: "batch-tool-budget", ConversationID: "conversation-a", TurnID: "turn-a",
			Source: memory.KnowledgeIngestSource{
				ID: "source-a", Title: "来源", URL: "https://a.example/tool-budget",
				Snippet: "作品当前公开状态信息。", Rank: 1, FetchedAtUnixMS: 1,
			},
		},
	}
	if err := engine.executeKnowledgeIngestClaim(store, claim); err == nil {
		t.Fatal("executeKnowledgeIngestClaim() error = nil")
	}
	if len(modelPort.requests) != 1 || len(store.commits) != 0 {
		t.Fatalf("model calls=%d commits=%d", len(modelPort.requests), len(store.commits))
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
	store := retentionKnowledgeStore{calls: &knowledgeCalls}
	service.memory.retention.knowledge = store
	service.memory.retention.knowledgeLease = store
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
	store := retentionKnowledgeStore{calls: &calls}
	service.memory.retention.knowledge = store
	service.memory.retention.knowledgeLease = store
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
