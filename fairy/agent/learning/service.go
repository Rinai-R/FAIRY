package learning

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fairy/context/character"
	"fairy/context/knowledge"
	"fairy/context/memory/extraction"
	"fairy/runtime/config"
	"fairy/runtime/model"
)

const (
	retentionJobCapacity               = 8
	retentionIdleCapacity              = 256
	knowledgeLearningCapacity          = 32
	maxKnowledgeAgentToolCalls         = 8
	knowledgeAgentMaximumCallsPerRound = 8
)

var (
	ErrClosed             = errors.New("retention background runtime is closed")
	ErrOverloaded         = errors.New("retention background capacity exhausted")
	ErrRuntimeUnavailable = errors.New("retention runtime is unavailable")
)

type ExtractionStore interface {
	PendingExtractionTurnCount(conversationID string) (uint64, error)
	ClaimExtractionBatch(conversationID string, limit int) (*extraction.BatchInput, error)
	FailExtractionBatch(batchID, code, message string, retryable bool) error
	CommitMemoryMutations(batchID, characterID string, allowedMemoryIDs []string, mutations []extraction.Mutation) ([]extraction.MutationResult, error)
}

type KnowledgeStore interface {
	SearchKnowledgeForIngestContext(context.Context, string, int) ([]knowledge.Retrieved, error)
	CommitKnowledgeDocumentActionsContext(context.Context, knowledge.IngestTask, knowledge.Document, []string, []knowledge.DocumentAction) (int, error)
}

type ModelExecutor interface {
	ExecuteRequestContext(context.Context, model.CompiledPromptRequest) ([]model.StreamEvent, error)
}

type ConfigSource interface {
	ModelConnection() (config.ModelConnection, error)
}

type Options struct {
	Extraction         ExtractionStore
	Knowledge          KnowledgeStore
	Documents          knowledge.DocumentFetcher
	Model              ModelExecutor
	Config             ConfigSource
	Character          func(string) (character.Record, error)
	ObserveError       func(error)
	ClearError         func()
	RecordKnowledgeRun func(knowledge.IngestTask, []model.StreamEvent, []model.LaneModelUsage)
}

type KnowledgeOpportunity struct {
	Task knowledge.IngestTask
}

type CompletedTurn struct {
	ConversationID         string
	ExtractPersonalMemory  bool
	KnowledgeOpportunities []KnowledgeOpportunity
}

type retentionIdleTimer struct {
	owner  uint64
	cancel context.CancelFunc
}

type Service struct {
	options           Options
	closed            atomic.Bool
	jobs              atomic.Int64
	admission         sync.Mutex
	slots             chan struct{}
	wg                sync.WaitGroup
	idleMu            sync.Mutex
	idle              map[string]retentionIdleTimer
	idleCapacity      int
	idleOwner         uint64
	closeOnce         sync.Once
	coverageCommitted sync.Map
	knowledgeQueue    chan knowledge.IngestTask
	workerCtx         context.Context
	workerCancel      context.CancelFunc
}

func New(options Options) *Service {
	return newWithCapacity(options, retentionJobCapacity, retentionIdleCapacity)
}

func newWithCapacity(options Options, jobCapacity, idleCapacity int) *Service {
	if jobCapacity < 1 {
		jobCapacity = 1
	}
	if idleCapacity < 1 {
		idleCapacity = 1
	}
	workerCtx, workerCancel := context.WithCancel(context.Background())
	return &Service{
		options:        options,
		slots:          make(chan struct{}, jobCapacity),
		idle:           make(map[string]retentionIdleTimer),
		idleCapacity:   idleCapacity,
		knowledgeQueue: make(chan knowledge.IngestTask, knowledgeLearningCapacity),
		workerCtx:      workerCtx,
		workerCancel:   workerCancel,
	}
}

func (e *Service) Start() {
	if e == nil || e.closed.Load() {
		return
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		for {
			select {
			case <-e.workerCtx.Done():
				return
			case task := <-e.knowledgeQueue:
				if e.options.Knowledge == nil {
					e.observeError(ErrRuntimeUnavailable)
					continue
				}
				if err := e.executeKnowledgeIngestTaskContext(e.workerCtx, e.options.Knowledge, task); err != nil {
					if e.workerCtx.Err() != nil && errors.Is(err, e.workerCtx.Err()) {
						return
					}
					e.observeError(err)
					continue
				}
				e.clearError()
			}
		}
	}()
}

func (e *Service) AdmitKnowledgeTasks(tasks []knowledge.IngestTask) {
	if e == nil || e.closed.Load() {
		return
	}
	for _, task := range tasks {
		select {
		case e.knowledgeQueue <- task:
		default:
			e.observeError(ErrOverloaded)
		}
	}
}

func (e *Service) ObserveCompletedTurn(completed CompletedTurn) {
	if e == nil {
		return
	}
	if completed.ExtractPersonalMemory {
		e.ScheduleExtraction(completed.ConversationID)
	}
	for _, opportunity := range completed.KnowledgeOpportunities {
		e.AdmitKnowledgeTasks([]knowledge.IngestTask{opportunity.Task})
	}
}

// ScheduleCompaction admits one capacity-bounded post-Turn compaction worker.
// The compaction operation is injected by Turn so retention owns scheduling
// without importing reactive orchestration.
func (e *Service) ScheduleCompaction(job func()) error {
	return e.run(job)
}

// ScheduleDeferred admits a bounded non-blocking task selected by another
// orchestrator. Core exposes this through a separate adapter so Turn does not
// mistake it for the post-Turn retention contract.
func (e *Service) ScheduleDeferred(job func()) error {
	return e.run(job)
}

// run starts one capacity-bounded background job and tracks it until
// completion. Admission never waits for a slot.
func (e *Service) run(job func()) error {
	if e == nil || job == nil {
		return ErrClosed
	}
	e.admission.Lock()
	if e.closed.Load() {
		e.admission.Unlock()
		return ErrClosed
	}
	select {
	case e.slots <- struct{}{}:
	default:
		e.admission.Unlock()
		return ErrOverloaded
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

func (e *Service) runContext(job func(context.Context)) error {
	if e == nil || job == nil || e.workerCtx == nil {
		return ErrClosed
	}
	return e.run(func() { job(e.workerCtx) })
}

func (e *Service) ActiveJobs() int64 {
	if e == nil {
		return 0
	}
	return e.jobs.Load()
}

func (e *Service) ScheduleExtraction(conversationID string) {
	if e == nil || e.closed.Load() {
		return
	}
	store := e.options.Extraction
	if store == nil {
		return
	}
	e.cancelIdle(conversationID)
	pending, err := store.PendingExtractionTurnCount(conversationID)
	if err != nil {
		e.observeError(err)
		return
	}
	if pending >= extractionThreshold {
		if err := e.runContext(func(ctx context.Context) { e.claimAndRunExtraction(ctx, conversationID) }); err != nil {
			e.observeError(err)
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
		if err := e.runContext(func(ctx context.Context) { e.claimAndRunExtraction(ctx, conversationID) }); err != nil {
			e.observeError(err)
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
			if err := e.runContext(func(ctx context.Context) { e.claimAndRunExtraction(ctx, conversationID) }); err != nil {
				e.observeError(err)
			}
		}
	}()
}

func (e *Service) claimAndRunExtraction(ctx context.Context, conversationID string) {
	if e == nil || e.closed.Load() {
		return
	}
	if ctx == nil || ctx.Err() != nil {
		return
	}
	store := e.options.Extraction
	if store == nil {
		return
	}
	batch, err := store.ClaimExtractionBatch(conversationID, extractionBatchLimit)
	if err != nil {
		e.observeError(err)
		return
	}
	if batch == nil {
		return
	}
	if err := e.executeExtractionBatch(ctx, store, batch); err != nil {
		if failErr := store.FailExtractionBatch(batch.BatchID, "EXTRACTION_BATCH_FAILED", err.Error(), false); failErr != nil {
			e.observeError(failErr)
			return
		}
		e.observeError(err)
		return
	}
	e.clearError()
}

func (e *Service) executeExtractionBatch(ctx context.Context, store ExtractionStore, batch *extraction.BatchInput) error {
	if ctx == nil {
		return errors.New("extraction context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if batch == nil {
		return errors.New("extraction batch is required")
	}
	if e.options.Character == nil {
		return ErrRuntimeUnavailable
	}
	record, err := e.options.Character(batch.CharacterID)
	if err != nil {
		return err
	}
	modelPort := e.options.Model
	if modelPort == nil {
		return ErrRuntimeUnavailable
	}
	configSource := e.options.Config
	if configSource == nil {
		return ErrRuntimeUnavailable
	}
	connection, err := configSource.ModelConnection()
	if err != nil {
		return err
	}
	envelope := privateDiscoveryEnvelope(*batch)
	discoveryInput, err := buildDiscoveryInput(envelope)
	if err != nil {
		return err
	}
	discoveryCacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		discoveryCacheKey = model.LaneCacheKey(batch.ConversationID, model.PromptLaneLearningDiscovery)
	}
	discoveryCacheInput := model.NewCacheKeyInput(model.PromptLaneLearningDiscovery, connection.Model, batch.ConversationID, character.LearningDiscoveryInstructions)
	discoveryCacheInput.CharacterRevision = record.Revision
	discoveryEvents, err := modelPort.ExecuteRequestContext(ctx, model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneLearningDiscovery, Model: connection.Model, Instructions: character.LearningDiscoveryInstructions,
			MaxOutputTokens: character.LearningDiscoveryMaxOutputTokens, PromptCacheKey: discoveryCacheKey,
		},
		Input: discoveryInput, CacheInput: &discoveryCacheInput,
	})
	if err != nil {
		return err
	}
	discovery, err := parseDiscoveryOutput(model.CollectTextFromEvents(discoveryEvents), envelope)
	if err != nil {
		return err
	}
	candidates := personalDiscoveryCandidates(discovery)
	if len(candidates) == 0 {
		return e.commitExtractionMutations(ctx, batch.ConversationID, func() ([]extraction.MutationResult, error) {
			return store.CommitMemoryMutations(batch.BatchID, batch.CharacterID, nil, nil)
		})
	}
	input, aliases, err := buildExtractInput(*batch, candidates)
	if err != nil {
		return err
	}
	cacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(batch.ConversationID, model.PromptLaneExtract)
	}
	cacheInput := model.NewCacheKeyInput(model.PromptLaneExtract, connection.Model, batch.ConversationID, character.ExtractInstructions)
	cacheInput.CharacterRevision = record.Revision
	events, err := modelPort.ExecuteRequestContext(ctx, model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneExtract, Model: connection.Model, Instructions: character.ExtractInstructions,
			MaxOutputTokens: character.ExtractMaxOutputTokens, PromptCacheKey: cacheKey,
		},
		Input: input, CacheInput: &cacheInput,
	})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	allowedTurnIDs := make(map[string]struct{}, len(batch.Turns))
	for _, turn := range batch.Turns {
		allowedTurnIDs[turn.TurnID] = struct{}{}
	}
	output, err := parseMemoryMutationOutput(model.CollectTextFromEvents(events), aliases, allowedTurnIDs)
	if err != nil {
		return err
	}
	allowed := make([]string, 0, len(batch.ExistingMemories))
	for _, item := range batch.ExistingMemories {
		allowed = append(allowed, item.ID)
	}
	return e.commitExtractionMutations(ctx, batch.ConversationID, func() ([]extraction.MutationResult, error) {
		return store.CommitMemoryMutations(batch.BatchID, batch.CharacterID, allowed, output.Mutations)
	})
}

func (e *Service) commitExtractionMutations(
	ctx context.Context,
	conversationID string,
	commit func() ([]extraction.MutationResult, error),
) error {
	if e == nil || ctx == nil || commit == nil {
		return ErrClosed
	}
	e.admission.Lock()
	defer e.admission.Unlock()
	if e.closed.Load() {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	results, err := commit()
	if err != nil {
		return err
	}
	if len(results) > 0 {
		e.coverageCommitted.Store(conversationID, struct{}{})
	}
	return nil
}

func (e *Service) TakeCommittedCoverage(conversationID string) bool {
	if e == nil {
		return false
	}
	_, loaded := e.coverageCommitted.LoadAndDelete(conversationID)
	return loaded
}

func (e *Service) executeKnowledgeIngestTaskContext(ctx context.Context, store KnowledgeStore, task knowledge.IngestTask) error {
	if e == nil {
		return ErrClosed
	}
	if ctx == nil {
		return errors.New("knowledge ingest context is required")
	}
	if e.options.Documents == nil {
		return errors.New("knowledge document fetcher is unavailable")
	}
	document, err := e.options.Documents.FetchSource(ctx, task.Source)
	if err != nil {
		return err
	}
	document.ReconcilerRevision = knowledge.ReconcilerRevision(
		character.KnowledgeReconcileInstructions,
		knowledge.AgentContractRevision,
	)
	configSource := e.options.Config
	if configSource == nil {
		return ErrRuntimeUnavailable
	}
	connection, err := configSource.ModelConnection()
	if err != nil {
		return err
	}
	modelPort := e.options.Model
	if modelPort == nil {
		return ErrRuntimeUnavailable
	}
	discoveryEnvelope := knowledgeDiscoveryEnvelope(task, document)
	discoveryInput, err := buildDiscoveryInput(discoveryEnvelope)
	if err != nil {
		return err
	}
	discoveryCacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		discoveryCacheKey = model.LaneCacheKey(task.ConversationID, model.PromptLaneLearningDiscovery)
	}
	discoveryCacheInput := model.NewCacheKeyInput(model.PromptLaneLearningDiscovery, connection.Model, task.ConversationID, character.LearningDiscoveryInstructions)
	discoveryEvents, err := modelPort.ExecuteRequestContext(ctx, model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneLearningDiscovery, Model: connection.Model, Instructions: character.LearningDiscoveryInstructions,
			MaxOutputTokens: character.LearningDiscoveryMaxOutputTokens, PromptCacheKey: discoveryCacheKey,
		},
		Input: discoveryInput, CacheInput: &discoveryCacheInput,
	})
	if err != nil {
		return err
	}
	discovery, err := parseDiscoveryOutput(model.CollectTextFromEvents(discoveryEvents), discoveryEnvelope)
	if err != nil {
		return err
	}
	candidates := knowledgeDiscoveryCandidates(discovery)
	if len(candidates) == 0 {
		_, err := store.CommitKnowledgeDocumentActionsContext(ctx, task, document, nil, nil)
		return err
	}
	if err := knowledge.ValidateInitialAgentBudget(
		document,
		connection.ContextWindowTokens,
		character.KnowledgeReconcileMaxOutputTokens,
	); err != nil {
		return err
	}
	input, err := knowledge.BuildAgentInput(task, document, candidates)
	if err != nil {
		return err
	}
	cacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(task.ConversationID, model.PromptLaneKnowledgeReconcile)
	}
	cacheInput := model.NewCacheKeyInput(
		model.PromptLaneKnowledgeReconcile,
		connection.Model,
		task.ConversationID,
		character.KnowledgeReconcileInstructions,
	)
	aliases := knowledge.NewAliasSet()
	seenCallIDs := make(map[string]struct{})
	toolCalls := 0
	for round := 0; round <= maxKnowledgeAgentToolCalls; round++ {
		if err := knowledge.ValidateAgentPromptBudget(
			input,
			character.KnowledgeReconcileInstructions,
			connection.ContextWindowTokens,
			character.KnowledgeReconcileMaxOutputTokens,
		); err != nil {
			return err
		}
		events, err := modelPort.ExecuteRequestContext(ctx, model.CompiledPromptRequest{
			Shape: model.ModelRequestShape{
				Lane: model.PromptLaneKnowledgeReconcile, Model: connection.Model,
				Instructions:    character.KnowledgeReconcileInstructions,
				MaxOutputTokens: character.KnowledgeReconcileMaxOutputTokens,
				PromptCacheKey:  cacheKey,
			},
			Input: input, Tools: []model.ToolSpec{knowledge.SearchToolSpec()},
			CacheInput: &cacheInput,
		})
		if err != nil {
			return err
		}
		usage := model.LaneUsageFromEvents(model.PromptLaneKnowledgeReconcile, events, 0)
		if e.options.RecordKnowledgeRun != nil {
			e.options.RecordKnowledgeRun(task, events, usage)
		}
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
				if call.Name != knowledge.SearchToolName || call.CallID == "" {
					return errors.New("knowledge agent requested an unknown tool")
				}
				if _, duplicate := seenCallIDs[call.CallID]; duplicate {
					return errors.New("knowledge agent repeated a tool call ID")
				}
				seenCallIDs[call.CallID] = struct{}{}
				query, err := knowledge.ParseSearchArguments(call.Arguments)
				if err != nil {
					return err
				}
				candidates, err := store.SearchKnowledgeForIngestContext(ctx, query, knowledge.MaxSearchCandidates)
				if err != nil {
					return err
				}
				toolItems, err := knowledge.BuildSearchToolItems(call, candidates, aliases)
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
		actions, err := knowledge.ParseAgentOutput(text, document, aliases)
		if err != nil {
			return err
		}
		if _, err := store.CommitKnowledgeDocumentActionsContext(
			ctx,
			task,
			document,
			aliases.SuppliedIDs(),
			actions,
		); err != nil {
			return err
		}
		return nil
	}
	return errors.New("knowledge agent did not produce final output within its bounded loop")
}

func (e *Service) Close() {
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

func (e *Service) cancelIdle(conversationID string) {
	e.idleMu.Lock()
	timer := e.idle[conversationID]
	delete(e.idle, conversationID)
	e.idleMu.Unlock()
	if timer.cancel != nil {
		timer.cancel()
	}
}

func (e *Service) observeError(err error) {
	if e != nil && err != nil && e.options.ObserveError != nil {
		e.options.ObserveError(err)
	}
}

func (e *Service) clearError() {
	if e != nil && e.options.ClearError != nil {
		e.options.ClearError()
	}
}
