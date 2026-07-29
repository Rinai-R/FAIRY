package companion

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fairy/memory"
	"fairy/model"
	"fairy/persona"
)

const (
	knowledgeIngestClaimLimit = 1
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
	leaseStore := e.service.memory.retention.knowledgeLease
	if store == nil || leaseStore == nil {
		return
	}
	if ready, ok := store.(interface{ KnowledgeIngestReady() bool }); ok && !ready.KnowledgeIngestReady() {
		return
	}
	for {
		claims, err := store.ClaimKnowledgeIngestTasksContext(e.workerCtx, knowledgeIngestClaimLimit)
		if err != nil {
			if workerErr := e.workerCtx.Err(); workerErr != nil && errors.Is(err, workerErr) {
				return
			}
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
			if err := e.runKnowledgeIngestClaim(store, leaseStore, claim); err != nil {
				if workerErr := e.workerCtx.Err(); workerErr != nil && errors.Is(err, workerErr) {
					if releaseErr := leaseStore.ReleaseClaimedKnowledgeIngestJob(claim.JobID); releaseErr != nil {
						e.service.setBackgroundError(errors.Join(err, releaseErr))
					}
					return
				}
				var transient transientKnowledgeIngestError
				if errors.As(err, &transient) {
					if retryErr := store.RetryClaimedKnowledgeIngestJob(claim.JobID, transient.category, err.Error()); retryErr != nil {
						e.service.setBackgroundError(errors.Join(err, retryErr))
					} else {
						e.service.setBackgroundError(err)
					}
					continue
				}
				if failErr := store.FailClaimedKnowledgeIngestJob(claim.JobID, err.Error()); failErr != nil {
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

func (e *retentionEngine) runKnowledgeIngestClaim(store knowledgeIngestStore, leaseStore knowledgeIngestLeaseStore, claim memory.KnowledgeIngestClaim) error {
	leaseDuration := leaseStore.KnowledgeIngestLeaseDuration()
	if leaseDuration <= 0 {
		return errors.New("knowledge ingest lease duration is invalid")
	}
	renewInterval := leaseDuration / 3
	if renewInterval <= 0 {
		renewInterval = leaseDuration
	}
	claimCtx, cancel := context.WithCancel(e.workerCtx)
	renewalDone := make(chan error, 1)
	go func() {
		timer := time.NewTimer(renewInterval)
		defer timer.Stop()
		for {
			select {
			case <-claimCtx.Done():
				renewalDone <- nil
				return
			case <-timer.C:
				if err := leaseStore.RenewKnowledgeIngestLeaseContext(claimCtx, claim.JobID); err != nil {
					if claimCtx.Err() != nil {
						renewalDone <- nil
						return
					}
					cancel()
					renewalDone <- transientKnowledgeIngestError{category: "lease_renewal", err: err}
					return
				}
				timer.Reset(renewInterval)
			}
		}
	}()
	executionErr := e.executeKnowledgeIngestClaimContext(claimCtx, store, claim)
	cancel()
	renewalErr := <-renewalDone
	if executionErr == nil {
		return nil
	}
	if renewalErr != nil {
		return renewalErr
	}
	return executionErr
}

func (e *retentionEngine) executeKnowledgeIngestClaim(store knowledgeIngestStore, claim memory.KnowledgeIngestClaim) error {
	return e.executeKnowledgeIngestClaimContext(e.workerCtx, store, claim)
}

func (e *retentionEngine) executeKnowledgeIngestClaimContext(ctx context.Context, store knowledgeIngestStore, claim memory.KnowledgeIngestClaim) error {
	if e == nil || e.service == nil {
		return errRetentionClosed
	}
	if ctx == nil {
		return errors.New("knowledge ingest context is required")
	}
	if e.service.knowledgeDocuments == nil {
		return errors.New("knowledge document fetcher is unavailable")
	}
	document, err := e.service.knowledgeDocuments.FetchSource(ctx, claim.Task.Source)
	if err != nil {
		if errors.Is(err, errKnowledgeFetchTransient) {
			return transientKnowledgeIngestError{category: "document_fetch", err: err}
		}
		return err
	}
	document.ReconcilerRevision = knowledgeReconcilerRevision(
		persona.KnowledgeReconcileInstructions,
		knowledgeAgentContractRevision,
	)
	needsExtraction, err := store.KnowledgeDocumentNeedsExtractionContext(ctx, claim.JobID, claim.Task.ID, document)
	if err != nil {
		return transientKnowledgeIngestError{category: "document_state", err: err}
	}
	if !needsExtraction {
		return nil
	}
	configSource := e.service.configSource()
	if configSource == nil {
		return ErrTurnRuntimeUnavailable
	}
	connection, err := configSource.ModelConnection()
	if err != nil {
		return err
	}
	if err := validateInitialKnowledgeAgentBudget(
		document,
		connection.ContextWindowTokens,
		persona.KnowledgeReconcileMaxOutputTokens,
	); err != nil {
		return err
	}
	input, err := buildKnowledgeAgentInput(claim.Task, document)
	if err != nil {
		return err
	}
	cacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(claim.Task.ConversationID, model.PromptLaneKnowledgeReconcile)
	}
	cacheInput := model.NewCacheKeyInput(
		model.PromptLaneKnowledgeReconcile,
		connection.Model,
		claim.Task.ConversationID,
		persona.KnowledgeReconcileInstructions,
	)
	modelPort := e.service.modelPort()
	if modelPort == nil {
		return ErrTurnRuntimeUnavailable
	}
	aliases := newKnowledgeAliasSet()
	seenCallIDs := make(map[string]struct{})
	toolCalls := 0
	for round := 0; round <= maxKnowledgeAgentToolCalls; round++ {
		if err := validateKnowledgeAgentPromptBudget(
			input,
			persona.KnowledgeReconcileInstructions,
			connection.ContextWindowTokens,
			persona.KnowledgeReconcileMaxOutputTokens,
		); err != nil {
			return err
		}
		events, err := modelPort.ExecuteRequestContext(ctx, model.CompiledPromptRequest{
			Shape: model.ModelRequestShape{
				Lane: model.PromptLaneKnowledgeReconcile, Model: connection.Model,
				Instructions:    persona.KnowledgeReconcileInstructions,
				MaxOutputTokens: persona.KnowledgeReconcileMaxOutputTokens,
				PromptCacheKey:  cacheKey,
			},
			Input: input, Tools: []model.ToolSpec{knowledgeSearchToolSpec()},
			CacheInput: &cacheInput,
		})
		if err != nil {
			return transientKnowledgeIngestError{category: "model_provider", err: err}
		}
		usage := model.LaneUsageFromEvents(model.PromptLaneKnowledgeReconcile, events, 0)
		e.service.appendRuntimeLedger(
			claim.Task.ConversationID,
			claim.Task.TurnID,
			runtimeLedgerEventModel,
			turnStateCompleted,
			"",
			runtimeKnowledgeIngestLedgerMetadata(events, usage, claim.Task.ID),
		)
		calls := model.FunctionCallsFromEvents(events)
		text := model.CollectTextFromEvents(events)
		if len(calls) > 0 {
			if strings.TrimSpace(text) != "" {
				return errors.New("knowledge agent mixed tool calls with final output")
			}
			if len(calls) > knowledgeAgentMaximumCallsPerRound ||
				toolCalls+len(calls) > maxKnowledgeAgentToolCalls {
				return errors.New("knowledge agent tool call budget exceeded")
			}
			for _, call := range calls {
				if call.Name != knowledgeSearchToolName || call.CallID == "" {
					return errors.New("knowledge agent requested an unknown tool")
				}
				if _, duplicate := seenCallIDs[call.CallID]; duplicate {
					return errors.New("knowledge agent repeated a tool call ID")
				}
				seenCallIDs[call.CallID] = struct{}{}
				query, err := parseKnowledgeSearchArguments(call.Arguments)
				if err != nil {
					return err
				}
				candidates, err := store.SearchKnowledgeForIngestContext(ctx, query, memory.MaxKnowledgeSearchCandidates)
				if err != nil {
					return transientKnowledgeIngestError{category: "knowledge_search", err: err}
				}
				toolItems, err := buildKnowledgeSearchToolItems(call, candidates, aliases)
				if err != nil {
					return err
				}
				input = append(input, toolItems...)
				toolCalls++
			}
			continue
		}
		if strings.TrimSpace(text) == "" {
			return errors.New("knowledge agent returned neither tool calls nor final output")
		}
		actions, err := parseKnowledgeAgentOutput(text, document, aliases)
		if err != nil {
			return err
		}
		if _, err := store.CommitKnowledgeDocumentActionsContext(
			ctx,
			claim.JobID,
			claim.Task.ID,
			document,
			aliases.suppliedIDs(),
			actions,
		); err != nil {
			return transientKnowledgeIngestError{category: "document_commit", err: err}
		}
		return nil
	}
	return errors.New("knowledge agent did not produce final output within its bounded loop")
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
