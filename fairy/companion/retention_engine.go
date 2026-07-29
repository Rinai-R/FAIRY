package companion

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"fairy/memory"
	"fairy/model"
	"fairy/persona"
)

const (
	knowledgeIngestBatchLimit = 8
	knowledgeIngestPoll       = 2 * time.Second
	retentionJobCapacity      = 8
	retentionIdleCapacity     = 256
)

var (
	errRetentionClosed     = errors.New("retention background runtime is closed")
	errRetentionOverloaded = errors.New("retention background capacity exhausted")
)

type transientKnowledgeIngestError struct {
	category string
	err      error
}

func (e transientKnowledgeIngestError) Error() string { return e.err.Error() }
func (e transientKnowledgeIngestError) Unwrap() error { return e.err }

type retentionIdleTimer struct {
	owner  uint64
	cancel context.CancelFunc
}

type retentionEngine struct {
	service       *CompanionService
	closed        atomic.Bool
	jobs          atomic.Int64
	admission     sync.Mutex
	slots         chan struct{}
	wg            sync.WaitGroup
	idleMu        sync.Mutex
	idle          map[string]retentionIdleTimer
	idleCapacity  int
	idleOwner     uint64
	closeOnce     sync.Once
	knowledgeWake chan struct{}
	workerCtx     context.Context
	workerCancel  context.CancelFunc
}

func newRetentionEngine(service *CompanionService) *retentionEngine {
	return newRetentionEngineWithCapacity(service, retentionJobCapacity, retentionIdleCapacity)
}

func newRetentionEngineWithCapacity(service *CompanionService, jobCapacity, idleCapacity int) *retentionEngine {
	if jobCapacity < 1 {
		jobCapacity = 1
	}
	if idleCapacity < 1 {
		idleCapacity = 1
	}
	workerCtx, workerCancel := context.WithCancel(context.Background())
	return &retentionEngine{
		service:       service,
		slots:         make(chan struct{}, jobCapacity),
		idle:          make(map[string]retentionIdleTimer),
		idleCapacity:  idleCapacity,
		knowledgeWake: make(chan struct{}, 1),
		workerCtx:     workerCtx,
		workerCancel:  workerCancel,
	}
}

func (e *retentionEngine) start() {
	if e == nil || e.closed.Load() {
		return
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ticker := time.NewTicker(knowledgeIngestPoll)
		defer ticker.Stop()
		e.drainKnowledgeIngest()
		for {
			select {
			case <-e.workerCtx.Done():
				return
			case <-e.knowledgeWake:
				e.drainKnowledgeIngest()
			case <-ticker.C:
				e.drainKnowledgeIngest()
			}
		}
	}()
}

func (e *retentionEngine) wakeKnowledgeIngest() {
	if e == nil || e.closed.Load() {
		return
	}
	select {
	case e.knowledgeWake <- struct{}{}:
	default:
	}
}

func (e *retentionEngine) drainKnowledgeIngest() {
	if e == nil || e.closed.Load() || e.service == nil {
		return
	}
	store := e.service.memory.retention.knowledge
	if store == nil {
		return
	}
	if ready, ok := store.(interface{ KnowledgeIngestReady() bool }); ok && !ready.KnowledgeIngestReady() {
		return
	}
	for {
		claims, err := store.ClaimKnowledgeIngestBatches(knowledgeIngestBatchLimit)
		if err != nil {
			e.service.setBackgroundError(err)
			return
		}
		if len(claims) == 0 {
			return
		}
		for _, claim := range claims {
			if e.closed.Load() {
				return
			}
			if err := e.executeKnowledgeIngestClaim(store, claim); err != nil {
				var transient transientKnowledgeIngestError
				if errors.As(err, &transient) {
					if retryErr := store.RetryKnowledgeIngestBatch(claim.JobID, transient.category, err.Error()); retryErr != nil {
						e.service.setBackgroundError(errors.Join(err, retryErr))
					} else {
						e.service.setBackgroundError(err)
					}
					continue
				}
				if failErr := store.FailKnowledgeIngestBatch(claim.JobID, err.Error()); failErr != nil {
					e.service.setBackgroundError(errors.Join(err, failErr))
					continue
				}
				e.service.setBackgroundError(err)
				continue
			}
			e.service.clearBackgroundError()
		}
	}
}

// run starts one capacity-bounded background job and tracks it until
// completion. Admission never waits for a slot.
func (e *retentionEngine) run(job func()) error {
	if e == nil || job == nil {
		return errRetentionClosed
	}
	e.admission.Lock()
	if e.closed.Load() {
		e.admission.Unlock()
		return errRetentionClosed
	}
	select {
	case e.slots <- struct{}{}:
	default:
		e.admission.Unlock()
		return errRetentionOverloaded
	}
	e.wg.Add(1)
	e.jobs.Add(1)
	e.admission.Unlock()
	go func() {
		defer func() {
			<-e.slots
			e.jobs.Add(-1)
			e.wg.Done()
		}()
		job()
	}()
	return nil
}

func (e *retentionEngine) activeJobs() int64 {
	if e == nil {
		return 0
	}
	return e.jobs.Load()
}

func (e *retentionEngine) scheduleExtraction(conversationID string) {
	if e == nil || e.closed.Load() || e.service == nil {
		return
	}
	store := e.service.memory.retention.extraction
	if store == nil {
		return
	}
	e.cancelIdle(conversationID)
	pending, err := store.PendingExtractionTurnCount(conversationID)
	if err != nil {
		e.service.setBackgroundError(err)
		return
	}
	if pending >= extractionThreshold {
		if err := e.run(func() { e.claimAndRunExtraction(conversationID) }); err != nil {
			e.service.setBackgroundError(err)
		}
		return
	}
	if pending == 0 {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	e.idleMu.Lock()
	if e.closed.Load() {
		e.idleMu.Unlock()
		cancel()
		return
	}
	if len(e.idle) >= e.idleCapacity {
		e.idleMu.Unlock()
		cancel()
		if err := e.run(func() { e.claimAndRunExtraction(conversationID) }); err != nil {
			e.service.setBackgroundError(err)
		}
		return
	}
	e.idleOwner++
	owner := e.idleOwner
	e.idle[conversationID] = retentionIdleTimer{owner: owner, cancel: cancel}
	e.wg.Add(1)
	e.idleMu.Unlock()
	go func() {
		defer e.wg.Done()
		timer := time.NewTimer(extractionIdleSeconds * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			e.idleMu.Lock()
			current, exists := e.idle[conversationID]
			if !exists || current.owner != owner {
				e.idleMu.Unlock()
				return
			}
			delete(e.idle, conversationID)
			e.idleMu.Unlock()
			if err := e.run(func() { e.claimAndRunExtraction(conversationID) }); err != nil {
				e.service.setBackgroundError(err)
			}
		}
	}()
}

func (e *retentionEngine) claimAndRunExtraction(conversationID string) {
	if e == nil || e.closed.Load() || e.service == nil {
		return
	}
	store := e.service.memory.retention.extraction
	if store == nil {
		return
	}
	batch, err := store.ClaimExtractionBatch(conversationID, extractionBatchLimit)
	if err != nil {
		e.service.setBackgroundError(err)
		return
	}
	if batch == nil {
		return
	}
	if err := e.executeExtractionBatch(store, batch); err != nil {
		if failErr := store.FailExtractionBatch(batch.BatchID, "EXTRACTION_BATCH_FAILED", err.Error(), false); failErr != nil {
			e.service.setBackgroundError(failErr)
			return
		}
		e.service.setBackgroundError(err)
		return
	}
	e.service.clearBackgroundError()
}

func (e *retentionEngine) executeExtractionBatch(store extractionStore, batch *memory.ExtractionBatchInput) error {
	if batch == nil {
		return errors.New("extraction batch is required")
	}
	input, err := buildExtractInput(*batch)
	if err != nil {
		return err
	}
	record, err := e.service.activeCharacter(batch.CharacterID)
	if err != nil {
		return err
	}
	configSource := e.service.configSource()
	if configSource == nil {
		return ErrTurnRuntimeUnavailable
	}
	connection, err := configSource.ModelConnection()
	if err != nil {
		return err
	}
	cacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(batch.ConversationID, model.PromptLaneExtract)
	}
	cacheInput := model.NewCacheKeyInput(model.PromptLaneExtract, connection.Model, batch.ConversationID, persona.ExtractInstructions)
	cacheInput.CharacterRevision = record.Revision
	modelPort := e.service.modelPort()
	if modelPort == nil {
		return ErrTurnRuntimeUnavailable
	}
	events, err := modelPort.ExecuteRequestContext(context.Background(), model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneExtract, Model: connection.Model, Instructions: persona.ExtractInstructions,
			MaxOutputTokens: persona.ExtractMaxOutputTokens, PromptCacheKey: cacheKey,
		},
		Input: input, CacheInput: &cacheInput,
	})
	if err != nil {
		return transientKnowledgeIngestError{category: "model_provider", err: err}
	}
	output, err := parseMemoryMutationOutput(model.CollectTextFromEvents(events))
	if err != nil {
		return err
	}
	allowed := make([]string, 0, len(batch.ExistingMemories))
	for _, item := range batch.ExistingMemories {
		allowed = append(allowed, item.ID)
	}
	_, err = store.CommitMemoryMutations(batch.BatchID, batch.CharacterID, allowed, output.Mutations)
	return err
}

func (e *retentionEngine) executeKnowledgeIngestClaim(store knowledgeIngestStore, claim memory.KnowledgeIngestClaim) error {
	if e == nil || e.service == nil {
		return errRetentionClosed
	}
	if e.service.knowledgeDocuments == nil {
		return errors.New("knowledge document fetcher is unavailable")
	}
	if len(claim.Batch.Sources) != 1 {
		return errors.New("knowledge ingest source job must contain exactly one source")
	}
	document, err := e.service.knowledgeDocuments.FetchSource(e.workerCtx, claim.Batch.Sources[0])
	if err != nil {
		if errors.Is(err, errKnowledgeFetchTransient) {
			return transientKnowledgeIngestError{category: "document_fetch", err: err}
		}
		return err
	}
	documents := []memory.KnowledgeDocument{document}
	needsExtraction, err := store.KnowledgeDocumentsNeedExtraction(claim.JobID, claim.Batch.ID, documents)
	if err != nil {
		return transientKnowledgeIngestError{category: "document_state", err: err}
	}
	if !needsExtraction {
		return nil
	}
	input, err := buildKnowledgeIngestInput(claim.Batch, documents)
	if err != nil {
		return err
	}
	configSource := e.service.configSource()
	if configSource == nil {
		return ErrTurnRuntimeUnavailable
	}
	connection, err := configSource.ModelConnection()
	if err != nil {
		return err
	}
	cacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(claim.Batch.ConversationID, model.PromptLaneKnowledgeIngest)
	}
	cacheInput := model.NewCacheKeyInput(
		model.PromptLaneKnowledgeIngest,
		connection.Model,
		claim.Batch.ConversationID,
		persona.KnowledgeIngestInstructions,
	)
	modelPort := e.service.modelPort()
	if modelPort == nil {
		return ErrTurnRuntimeUnavailable
	}
	events, err := modelPort.ExecuteRequestContext(context.Background(), model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneKnowledgeIngest, Model: connection.Model,
			Instructions:    persona.KnowledgeIngestInstructions,
			MaxOutputTokens: persona.KnowledgeIngestMaxOutputTokens,
			PromptCacheKey:  cacheKey,
		},
		Input: input, CacheInput: &cacheInput,
	})
	if err != nil {
		return err
	}
	usage := model.LaneUsageFromEvents(model.PromptLaneKnowledgeIngest, events, 0)
	e.service.appendRuntimeLedger(
		claim.Batch.ConversationID,
		claim.Batch.TurnID,
		runtimeLedgerEventModel,
		turnStateCompleted,
		"",
		runtimeKnowledgeIngestLedgerMetadata(events, usage, claim.Batch.ID, len(claim.Batch.Sources)),
	)
	output, err := parseKnowledgeIngestOutput(model.CollectTextFromEvents(events), documents)
	if err != nil {
		return err
	}
	facts := make([]memory.KnowledgeIngestFact, 0, len(output.Facts))
	for _, fact := range output.Facts {
		facts = append(facts, memory.KnowledgeIngestFact{
			Subject: fact.Subject, Predicate: fact.Predicate, Value: fact.Value, Statement: fact.Statement,
			ConfidenceBasisPoints: fact.ConfidenceBasisPoints,
			EvidenceChunkIDs:      append([]string(nil), fact.EvidenceChunkIDs...),
		})
	}
	recalls := make([]memory.KnowledgeIngestRecall, len(facts))
	for index, fact := range facts {
		candidates, err := store.RecallKnowledgeForIngest(fact, memory.MaxKnowledgeIngestRecallCandidates)
		if err != nil {
			return transientKnowledgeIngestError{category: "knowledge_recall", err: err}
		}
		recalls[index] = memory.KnowledgeIngestRecall{
			FactIndex:  index,
			Candidates: append([]memory.RetrievedKnowledge(nil), candidates...),
		}
	}
	mutations := []memory.KnowledgeIngestMutation{}
	if len(facts) > 0 {
		reconcileInput, aliases, err := buildKnowledgeReconcileInput(claim.Batch.ID, facts, recalls)
		if err != nil {
			return err
		}
		reconcileCacheInput := model.NewCacheKeyInput(
			model.PromptLaneKnowledgeReconcile,
			connection.Model,
			claim.Batch.ConversationID,
			persona.KnowledgeReconcileInstructions,
		)
		reconcileCacheKey := ""
		if connection.Capabilities.PromptCacheKey {
			reconcileCacheKey = model.LaneCacheKey(claim.Batch.ConversationID, model.PromptLaneKnowledgeReconcile)
		}
		reconcileEvents, err := modelPort.ExecuteRequestContext(context.Background(), model.CompiledPromptRequest{
			Shape: model.ModelRequestShape{
				Lane: model.PromptLaneKnowledgeReconcile, Model: connection.Model,
				Instructions:    persona.KnowledgeReconcileInstructions,
				MaxOutputTokens: persona.KnowledgeReconcileMaxOutputTokens,
				PromptCacheKey:  reconcileCacheKey,
			},
			Input: reconcileInput, CacheInput: &reconcileCacheInput,
		})
		if err != nil {
			return transientKnowledgeIngestError{category: "model_provider", err: err}
		}
		reconcileUsage := model.LaneUsageFromEvents(model.PromptLaneKnowledgeReconcile, reconcileEvents, 0)
		e.service.appendRuntimeLedger(
			claim.Batch.ConversationID,
			claim.Batch.TurnID,
			runtimeLedgerEventModel,
			turnStateCompleted,
			"",
			runtimeKnowledgeIngestLedgerMetadata(reconcileEvents, reconcileUsage, claim.Batch.ID, len(claim.Batch.Sources)),
		)
		mutations, err = parseKnowledgeReconcileOutput(model.CollectTextFromEvents(reconcileEvents), facts, aliases)
		if err != nil {
			return err
		}
	}
	if _, err := store.CommitKnowledgeDocumentMutations(claim.JobID, claim.Batch.ID, documents, facts, recalls, mutations); err != nil {
		return transientKnowledgeIngestError{category: "document_commit", err: err}
	}
	return nil
}

func cloneKnowledgeIngestBatches(batches []memory.KnowledgeIngestBatch) []memory.KnowledgeIngestBatch {
	cloned := make([]memory.KnowledgeIngestBatch, len(batches))
	for index, batch := range batches {
		cloned[index] = batch
		cloned[index].Sources = append([]memory.KnowledgeIngestSource(nil), batch.Sources...)
	}
	return cloned
}

func (e *retentionEngine) close() {
	if e == nil {
		return
	}
	e.closeOnce.Do(func() {
		e.admission.Lock()
		e.closed.Store(true)
		e.admission.Unlock()
		if e.workerCancel != nil {
			e.workerCancel()
		}
		e.idleMu.Lock()
		idle := e.idle
		e.idle = make(map[string]retentionIdleTimer)
		e.idleMu.Unlock()
		for _, timer := range idle {
			if timer.cancel != nil {
				timer.cancel()
			}
		}
		e.wg.Wait()
	})
}

func (e *retentionEngine) cancelIdle(conversationID string) {
	e.idleMu.Lock()
	timer := e.idle[conversationID]
	delete(e.idle, conversationID)
	e.idleMu.Unlock()
	if timer.cancel != nil {
		timer.cancel()
	}
}
