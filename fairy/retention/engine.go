// Package retention owns post-turn background work and its lifecycle state.
package retention

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"fairy/character"
	"fairy/config"
	"fairy/extraction"
	"fairy/memory"
	"fairy/memory/semantic"
	"fairy/model"
	"fairy/persona"
)

const knowledgeIngestBatchLimit = 8

type ExtractionStore interface {
	PendingExtractionTurnCount(conversationID string) (uint64, error)
	ClaimExtractionBatch(conversationID string, limit int) (*memory.ExtractionBatchInput, error)
	FailExtractionBatch(batchID, code, message string, retryable bool) error
	CommitMemoryMutations(batchID string, characterID string, allowedMemoryIDs []string, mutations []memory.MemoryMutation) ([]memory.MemoryMutationResult, error)
	ProcessEmbeddingJobsWithVectorIndex(context.Context, semantic.Embedder, memory.VectorIndex, int) (memory.EmbeddingJobResult, error)
}

type KnowledgeIngestStore interface {
	EnqueueKnowledgeIngestSnapshots(snapshots []memory.KnowledgeIngestSnapshot) error
	ProcessKnowledgeIngestJobs(limit int) (int, error)
}

type VectorIndex interface {
	memory.VectorIndex
	memory.SemanticVectorIndex
}

// Host is the retention consumer contract. It intentionally exposes lifecycle
// capabilities instead of a storage aggregate or a CompanionService pointer.
type Host interface {
	ExtractionStore() ExtractionStore
	KnowledgeIngestStore() KnowledgeIngestStore
	ActiveCharacter(characterID string) (character.Record, error)
	ModelConnection() (config.ModelConnection, error)
	ExecuteModel(context.Context, model.CompiledPromptRequest) ([]model.StreamEvent, error)
	SemanticEmbedder() semantic.Embedder
	VectorIndex() VectorIndex
	SetBackgroundError(error)
	ClearBackgroundError()
}

type Engine struct {
	host      Host
	closed    atomic.Bool
	jobs      atomic.Int64
	admission sync.Mutex
	idleMu    sync.Mutex
	idle      map[string]context.CancelFunc
	closeOnce sync.Once
}

func NewEngine(host Host) *Engine {
	return &Engine{host: host, idle: make(map[string]context.CancelFunc)}
}

// Run starts one bounded background job and tracks it until completion.
// It returns false after Close, so shutdown cannot admit new work.
func (e *Engine) Run(job func()) bool {
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

func (e *Engine) ActiveJobs() int64 {
	if e == nil {
		return 0
	}
	return e.jobs.Load()
}

func (e *Engine) ScheduleExtraction(conversationID string) {
	if e == nil || e.closed.Load() || e.host == nil {
		return
	}
	store := e.host.ExtractionStore()
	if store == nil {
		return
	}
	e.cancelIdle(conversationID)
	pending, err := store.PendingExtractionTurnCount(conversationID)
	if err != nil {
		e.host.SetBackgroundError(err)
		return
	}
	if pending >= extraction.Threshold {
		e.Run(func() { e.claimAndRunExtraction(conversationID) })
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
		timer := time.NewTimer(extraction.IdleSeconds * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			e.idleMu.Lock()
			delete(e.idle, conversationID)
			e.idleMu.Unlock()
			e.Run(func() { e.claimAndRunExtraction(conversationID) })
		}
	}()
}

func (e *Engine) claimAndRunExtraction(conversationID string) {
	if e == nil || e.closed.Load() || e.host == nil {
		return
	}
	store := e.host.ExtractionStore()
	if store == nil {
		return
	}
	batch, err := store.ClaimExtractionBatch(conversationID, extraction.BatchLimit)
	if err != nil {
		e.host.SetBackgroundError(err)
		return
	}
	if batch == nil {
		return
	}
	if err := e.executeExtractionBatch(store, batch); err != nil {
		if failErr := store.FailExtractionBatch(batch.BatchID, "EXTRACTION_BATCH_FAILED", err.Error(), false); failErr != nil {
			e.host.SetBackgroundError(failErr)
			return
		}
		e.host.SetBackgroundError(err)
		return
	}
	e.host.ClearBackgroundError()
	if _, err := store.ProcessEmbeddingJobsWithVectorIndex(context.Background(), e.host.SemanticEmbedder(), e.host.VectorIndex(), extraction.EmbeddingPassLimit); err != nil {
		e.host.SetBackgroundError(err)
	}
}

func (e *Engine) executeExtractionBatch(store ExtractionStore, batch *memory.ExtractionBatchInput) error {
	if batch == nil {
		return errors.New("extraction batch is required")
	}
	input, err := extraction.BuildInput(*batch)
	if err != nil {
		return err
	}
	record, err := e.host.ActiveCharacter(batch.CharacterID)
	if err != nil {
		return err
	}
	connection, err := e.host.ModelConnection()
	if err != nil {
		return err
	}
	cacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		cacheKey = model.LaneCacheKey(batch.ConversationID, model.PromptLaneExtract)
	}
	cacheInput := model.NewCacheKeyInput(model.PromptLaneExtract, connection.Model, batch.ConversationID, persona.ExtractInstructions)
	cacheInput.CharacterRevision = record.Revision
	events, err := e.host.ExecuteModel(context.Background(), model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane: model.PromptLaneExtract, Model: connection.Model, Instructions: persona.ExtractInstructions,
			MaxOutputTokens: persona.ExtractMaxOutputTokens, PromptCacheKey: cacheKey,
		},
		Input: input, CacheInput: &cacheInput,
	})
	if err != nil {
		return err
	}
	output, err := extraction.ParseMutationOutput(model.CollectTextFromEvents(events))
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

func (e *Engine) ScheduleKnowledgeIngest(snapshots []memory.KnowledgeIngestSnapshot) {
	if e == nil || e.closed.Load() || e.host == nil || len(snapshots) == 0 {
		return
	}
	store := e.host.KnowledgeIngestStore()
	if store == nil {
		return
	}
	copySnapshots := append([]memory.KnowledgeIngestSnapshot(nil), snapshots...)
	e.Run(func() {
		if err := store.EnqueueKnowledgeIngestSnapshots(copySnapshots); err != nil {
			e.host.SetBackgroundError(err)
			return
		}
		if _, err := store.ProcessKnowledgeIngestJobs(knowledgeIngestBatchLimit); err != nil {
			e.host.SetBackgroundError(err)
		}
	})
}

func (e *Engine) Close() {
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

func (e *Engine) cancelIdle(conversationID string) {
	e.idleMu.Lock()
	cancel := e.idle[conversationID]
	delete(e.idle, conversationID)
	e.idleMu.Unlock()
	if cancel != nil {
		cancel()
	}
}
