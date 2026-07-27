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

const knowledgeIngestBatchLimit = 8

type retentionEngine struct {
	service   *CompanionService
	closed    atomic.Bool
	jobs      atomic.Int64
	admission sync.Mutex
	idleMu    sync.Mutex
	idle      map[string]context.CancelFunc
	closeOnce sync.Once
}

func newRetentionEngine(service *CompanionService) *retentionEngine {
	return &retentionEngine{service: service, idle: make(map[string]context.CancelFunc)}
}

// Run starts one bounded background job and tracks it until completion.
// It returns false after Close, so shutdown cannot admit new work.
func (e *retentionEngine) run(job func()) bool {
	if e == nil || job == nil {
		return false
	}
	e.admission.Lock()
	if e.closed.Load() {
		e.admission.Unlock()
		return false
	}
	e.jobs.Add(1)
	e.admission.Unlock()
	go func() {
		defer e.jobs.Add(-1)
		job()
	}()
	return true
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
		e.run(func() { e.claimAndRunExtraction(conversationID) })
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
	e.idle[conversationID] = cancel
	e.idleMu.Unlock()
	go func() {
		timer := time.NewTimer(extractionIdleSeconds * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			e.idleMu.Lock()
			delete(e.idle, conversationID)
			e.idleMu.Unlock()
			e.run(func() { e.claimAndRunExtraction(conversationID) })
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
	if _, err := store.ProcessEmbeddingJobsWithVectorIndex(context.Background(), e.service.semanticEmbedder, e.service.vectorIndex, extractionEmbeddingPassLimit); err != nil {
		e.service.setBackgroundError(err)
	}
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
		return ErrRespondRuntimeNotMigrated
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
		return ErrRespondRuntimeNotMigrated
	}
	events, err := modelPort.ExecuteRequestContext(context.Background(), model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneExtract, Model: connection.Model, Instructions: persona.ExtractInstructions,
			MaxOutputTokens: persona.ExtractMaxOutputTokens, PromptCacheKey: cacheKey,
		},
		Input: input, CacheInput: &cacheInput,
	})
	if err != nil {
		return err
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

func (e *retentionEngine) scheduleKnowledgeIngest(snapshots []memory.KnowledgeIngestSnapshot) {
	if e == nil || e.closed.Load() || e.service == nil || len(snapshots) == 0 {
		return
	}
	store := e.service.memory.retention.knowledge
	if store == nil {
		return
	}
	copySnapshots := append([]memory.KnowledgeIngestSnapshot(nil), snapshots...)
	e.run(func() {
		if err := store.EnqueueKnowledgeIngestSnapshots(copySnapshots); err != nil {
			e.service.setBackgroundError(err)
			return
		}
		if _, err := store.ProcessKnowledgeIngestJobs(knowledgeIngestBatchLimit); err != nil {
			e.service.setBackgroundError(err)
		}
	})
}

func (e *retentionEngine) close() {
	if e == nil {
		return
	}
	e.closeOnce.Do(func() {
		e.admission.Lock()
		e.closed.Store(true)
		e.admission.Unlock()
		e.idleMu.Lock()
		idle := e.idle
		e.idle = make(map[string]context.CancelFunc)
		e.idleMu.Unlock()
		for _, cancel := range idle {
			if cancel != nil {
				cancel()
			}
		}
	})
}

func (e *retentionEngine) cancelIdle(conversationID string) {
	e.idleMu.Lock()
	cancel := e.idle[conversationID]
	delete(e.idle, conversationID)
	e.idleMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
