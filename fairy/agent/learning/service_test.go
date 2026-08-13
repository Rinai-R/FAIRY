package learning

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"fairy/context/knowledge"
	"fairy/context/memory/extraction"
	"fairy/context/memory/personal"
	"fairy/runtime/embedding"
)

func TestRetentionEngineBoundsConcurrentJobs(t *testing.T) {
	engine := newWithCapacity(Options{}, 2, 1)
	defer engine.Close()
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	var active atomic.Int64
	var maximum atomic.Int64
	job := func() {
		current := active.Add(1)
		started <- struct{}{}
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		<-release
		active.Add(-1)
	}
	if err := engine.ScheduleCompaction(job); err != nil {
		t.Fatal(err)
	}
	if err := engine.ScheduleCompaction(job); err != nil {
		t.Fatal(err)
	}
	if err := engine.ScheduleCompaction(job); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("third admission error = %v", err)
	}
	<-started
	<-started
	close(release)
	engine.Close()
	if maximum.Load() != 2 {
		t.Fatalf("maximum active jobs = %d", maximum.Load())
	}
}

func TestKnowledgeLearningAdmissionIsBoundedAndNonBlocking(t *testing.T) {
	var backgroundErr atomic.Value
	engine := &Service{
		options:        Options{ObserveError: func(err error) { backgroundErr.Store(err) }},
		knowledgeQueue: make(chan knowledge.IngestTask, 1),
	}
	first := knowledge.IngestTask{ID: "first"}
	second := knowledge.IngestTask{ID: "second"}
	started := time.Now()
	engine.AdmitKnowledgeTasks([]knowledge.IngestTask{first, second})
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("knowledge admission blocked")
	}
	if len(engine.knowledgeQueue) != 1 || (<-engine.knowledgeQueue).ID != "first" {
		t.Fatal("knowledge queue did not retain the first task")
	}
	observed, _ := backgroundErr.Load().(error)
	if !errors.Is(observed, ErrOverloaded) {
		t.Fatalf("background error = %v", observed)
	}
}

func TestRetentionEngineCloseRejectsNewWork(t *testing.T) {
	engine := newWithCapacity(Options{}, 1, 1)
	engine.Close()
	if err := engine.ScheduleCompaction(func() {}); !errors.Is(err, ErrClosed) {
		t.Fatalf("run after close error = %v", err)
	}
}

func TestRetentionEngineCloseCancelsExtractionBeforeCommitBarrier(t *testing.T) {
	engine := newWithCapacity(Options{}, 1, 1)
	started := make(chan struct{})
	finished := make(chan struct{})
	var commitCalls atomic.Int64
	var commitErr error
	if err := engine.runContext(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		commitErr = engine.commitExtractionMutations(ctx, "conversation-1", func(context.Context) ([]extraction.MutationResult, error) {
			commitCalls.Add(1)
			return []extraction.MutationResult{{Status: "applied", MemoryID: "memory-1"}}, nil
		})
		close(finished)
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	engine.Close()
	<-finished
	if !errors.Is(commitErr, ErrClosed) || commitCalls.Load() != 0 {
		t.Fatalf("commit after close = calls:%d error:%v", commitCalls.Load(), commitErr)
	}
	if engine.TakeCommittedCoverage("conversation-1") {
		t.Fatal("shutdown extraction published committed coverage")
	}
}

func TestRetentionEngineCloseCancelsProviderBeforeDatabaseBarrier(t *testing.T) {
	engine := newWithCapacity(Options{}, 1, 1)
	started := make(chan struct{})
	finished := make(chan struct{})
	var commitErr error
	if err := engine.runContext(func(ctx context.Context) {
		commitErr = engine.commitExtractionMutations(ctx, "conversation-1", func(commitCtx context.Context) ([]extraction.MutationResult, error) {
			close(started)
			<-commitCtx.Done()
			return nil, commitCtx.Err()
		})
		close(finished)
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	closed := make(chan struct{})
	go func() {
		engine.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close blocked behind a cancellable extraction provider")
	}
	<-finished
	if !errors.Is(commitErr, context.Canceled) {
		t.Fatalf("provider cancellation error = %v, want context.Canceled", commitErr)
	}
}

func TestRetentionEngineRejectsLegacyProviderBeforeItCanBlockClose(t *testing.T) {
	provider := &blockingLegacySemanticEmbedder{called: make(chan struct{})}
	store, err := personal.NewSeekDBStore(new(sql.DB), time.Second, provider)
	if err != nil {
		t.Fatal(err)
	}
	engine := newWithCapacity(Options{}, 1, 1)
	started := make(chan struct{})
	finished := make(chan struct{})
	var commitErr error
	if err := engine.runContext(func(ctx context.Context) {
		commitErr = engine.commitExtractionMutations(ctx, "conversation-1", func(commitCtx context.Context) ([]extraction.MutationResult, error) {
			close(started)
			_, err := store.PrepareEmbeddingsContext(commitCtx, []string{"content"})
			return nil, err
		})
		close(finished)
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	closed := make(chan struct{})
	go func() {
		engine.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close blocked behind a legacy extraction provider")
	}
	<-finished
	if !errors.Is(commitErr, embedding.ErrSemanticCancellationUnsupported) {
		t.Fatalf("legacy provider commit error = %v", commitErr)
	}
	select {
	case <-provider.called:
		t.Fatal("legacy provider was invoked")
	default:
	}
}

type blockingLegacySemanticEmbedder struct {
	called chan struct{}
}

func (*blockingLegacySemanticEmbedder) Ready() bool { return true }
func (*blockingLegacySemanticEmbedder) Status() embedding.SemanticStatus {
	return embedding.SemanticStatusReady
}
func (*blockingLegacySemanticEmbedder) ModelID() string { return "legacy-space" }
func (*blockingLegacySemanticEmbedder) Dims() int       { return embedding.Dimensions }
func (embedder *blockingLegacySemanticEmbedder) Embed([]string) ([][]float32, error) {
	close(embedder.called)
	select {}
}

func TestExtractionExecutionFailuresReleaseClaimsForRetry(t *testing.T) {
	for _, executionErr := range []error{
		extraction.ErrExtractionClaimConflict,
		errors.New("transient settlement failure"),
		context.Canceled,
	} {
		t.Run(executionErr.Error(), func(t *testing.T) {
			store := &enrichmentFailureExtractionStore{
				batch: &extraction.BatchInput{BatchID: "batch-1", ConversationID: "conversation-1"},
			}
			var observed error
			service := newWithCapacity(Options{Extraction: store, ObserveError: func(err error) {
				observed = err
			}}, 1, 1)
			defer service.Close()

			service.handleExtractionExecutionFailure(store, store.batch, executionErr)

			if store.failCalls != 1 || store.failedBatchID != "batch-1" ||
				store.failedCode != "EXTRACTION_BATCH_FAILED" || !store.failedRetryable {
				t.Fatalf("retry release = calls:%d batch:%q code:%q retryable:%t",
					store.failCalls, store.failedBatchID, store.failedCode, store.failedRetryable)
			}
			if !errors.Is(observed, executionErr) {
				t.Fatalf("observed error = %v, want %v", observed, executionErr)
			}
		})
	}
}

func TestRetentionCoverageSignalIsProcessLocalAndSingleConsumption(t *testing.T) {
	engine := newWithCapacity(Options{}, 1, 1)
	defer engine.Close()
	engine.coverageCommitted.Store("conversation-1", struct{}{})
	if !engine.TakeCommittedCoverage("conversation-1") {
		t.Fatal("committed coverage signal was not available")
	}
	if engine.TakeCommittedCoverage("conversation-1") {
		t.Fatal("committed coverage signal was consumed more than once")
	}
	if engine.TakeCommittedCoverage("conversation-2") {
		t.Fatal("unknown conversation reported committed coverage")
	}
}

func TestExtractionEnrichmentFailureReleasesDurableClaim(t *testing.T) {
	claimErr := errors.New("projection unavailable")
	failErr := errors.New("release failed")
	for _, test := range []struct {
		name       string
		failErr    error
		wantJoined bool
	}{
		{name: "released"},
		{name: "release failure is preserved", failErr: failErr, wantJoined: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &enrichmentFailureExtractionStore{
				batch: &extraction.BatchInput{
					BatchID: "batch-1", ConversationID: "conversation-1", CharacterID: "character-1",
				},
				claimErr: claimErr,
				failErr:  test.failErr,
			}
			var observed error
			service := newWithCapacity(Options{
				Extraction: store,
				ObserveError: func(err error) {
					observed = err
				},
			}, 1, 1)
			defer service.Close()

			service.claimAndRunExtraction(context.Background(), "conversation-1")

			if store.failCalls != 1 || store.failedBatchID != "batch-1" ||
				store.failedCode != "EXTRACTION_ENRICHMENT_FAILED" ||
				store.failedMessage != claimErr.Error() || !store.failedRetryable {
				t.Fatalf("claim release = calls:%d batch:%q code:%q message:%q retryable:%t",
					store.failCalls, store.failedBatchID, store.failedCode,
					store.failedMessage, store.failedRetryable)
			}
			if !errors.Is(observed, claimErr) {
				t.Fatalf("observed error = %v", observed)
			}
			if errors.Is(observed, failErr) != test.wantJoined {
				t.Fatalf("release error preserved = %t, want %t: %v",
					errors.Is(observed, failErr), test.wantJoined, observed)
			}
			if store.commitCalls != 0 {
				t.Fatalf("commit calls after enrichment failure = %d", store.commitCalls)
			}
		})
	}
}

type enrichmentFailureExtractionStore struct {
	batch           *extraction.BatchInput
	claimErr        error
	failErr         error
	failCalls       int
	failedBatchID   string
	failedCode      string
	failedMessage   string
	failedRetryable bool
	commitCalls     int
}

func (*enrichmentFailureExtractionStore) PendingExtractionTurnCount(string) (uint64, error) {
	return 0, nil
}

func (s *enrichmentFailureExtractionStore) ClaimExtractionBatch(string, int) (*extraction.BatchInput, error) {
	return s.batch, s.claimErr
}

func (s *enrichmentFailureExtractionStore) FailExtractionBatch(
	batchID, code, message string,
	retryable bool,
) error {
	s.failCalls++
	s.failedBatchID = batchID
	s.failedCode = code
	s.failedMessage = message
	s.failedRetryable = retryable
	return s.failErr
}

func (s *enrichmentFailureExtractionStore) CommitClaimedMemoryMutationsContext(
	context.Context,
	*extraction.BatchInput,
	[]extraction.Mutation,
) ([]extraction.MutationResult, error) {
	s.commitCalls++
	return nil, nil
}
