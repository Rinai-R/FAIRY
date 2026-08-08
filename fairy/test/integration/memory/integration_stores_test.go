//go:build integration

package memory

import (
	"context"

	historycompaction "fairy/context/history/compaction"
	"fairy/context/history/expression"
	historyprojection "fairy/context/history/projection"
	historyruntime "fairy/context/history/runtime"
	history "fairy/context/history/transcript"
	"fairy/context/identity"
	"fairy/context/knowledge"
	memoryadmin "fairy/context/memory/admin"
	"fairy/context/memory/extraction"
	"fairy/context/memory/personal"
	"fairy/context/recall"
	"fairy/context/social"
	coredb "fairy/runtime/database"
	"fairy/runtime/embedding"
	"fairy/runtime/ledger"
	"fairy/transport/session"
)

// Legacy integration cases still live in this directory while their files are
// moved to the owning packages. These test-only names keep the migration
// compileable without exposing compatibility aliases from production packages.
type MessageRecord = history.MessageRecord
type ConversationActivity = history.ConversationActivity
type ConversationRecord = history.ConversationRecord
type ExpressionPart = expression.Part
type StickerSnapshot = expression.StickerSnapshot
type UsageLaneAggregate = ledger.UsageLaneAggregate
type CreateToolExecutionInput = ledger.CreateToolExecutionInput
type CompleteToolExecutionInput = ledger.CompleteToolExecutionInput
type TurnRuntimeEventInput = historyruntime.TurnRuntimeEventInput
type LaneContinuationRecord = historyruntime.LaneContinuationRecord
type ContextWindowRecord = historyruntime.ContextWindowRecord
type Store = personal.Store
type VectorRebuildResult = personal.VectorRebuildResult
type PromptProjectionState = historyprojection.State
type PromptProjectionOmission = historyprojection.Omission

const (
	testSemanticEmbeddingModelID = "BAAI/bge-m3"
	ExpressionUtterance          = expression.Utterance
	ExpressionSticker            = expression.Sticker
	PromptLaneRespond            = historyruntime.PromptLaneRespond
	PromptProjectionVersion      = historyprojection.Version
	ToolExecutionCompleted       = ledger.ToolExecutionCompleted
	ToolExecutionFailed          = ledger.ToolExecutionFailed
	ToolNameDesktopObserve       = ledger.ToolNameDesktopObserve
)

var (
	ErrEndpointBindingMismatch     = history.ErrEndpointBindingMismatch
	ErrOwnerIdentityNotFound       = identity.ErrOwnerIdentityNotFound
	ErrPromptWindowRevisionChanged = historycompaction.ErrPromptWindowRevisionChanged
)

func EncodePromptProjection(state historyprojection.State) ([]byte, error) {
	return historyprojection.Encode(state)
}

func PromptMessageText(message history.MessageRecord) string {
	return history.PromptMessageText(message)
}

func NewIdentityStore(pool *coredb.Pool) (*identity.Store, error) {
	return identity.NewStore(pool)
}

func NewMemoryServiceFromStore(store *Store) (*memoryadmin.Service, error) {
	return memoryadmin.NewServiceFromStore(store)
}

func desktopBinding() session.Binding {
	return session.Binding{
		Endpoint: session.EndpointDesktop,
		Facts: session.Facts{
			Audience:     session.AudienceSingle,
			Initiation:   session.InitiationDirect,
			Presentation: session.PresentationEmbodied,
		},
	}
}

// The integration suite predates the domain split and exercises workflows
// spanning several stores. Keep that composition in test code instead of
// recreating the former production God Store.
type integrationHistory struct{ *history.Store }
type integrationCompaction struct{ *historycompaction.Store }
type integrationRuntime struct{ *historyruntime.Store }
type integrationExtraction struct{ *extraction.Store }
type integrationKnowledge struct{ *knowledge.Store }
type integrationSocial struct{ *social.Store }
type integrationLedger struct{ *ledger.Store }

type memoryIntegrationStores struct {
	*Store
	integrationHistory
	integrationCompaction
	integrationRuntime
	integrationExtraction
	integrationKnowledge
	integrationSocial
	integrationLedger
}

func newMemoryIntegrationStores(pool *coredb.Pool) (*memoryIntegrationStores, error) {
	return newMemoryIntegrationStoresWithSemanticEmbedder(pool, nil)
}

func newMemoryIntegrationStoresWithEmbedder(pool *coredb.Pool, embedder embedding.SemanticEmbedder) (*memoryIntegrationStores, error) {
	return newMemoryIntegrationStoresWithSemanticEmbedder(pool, embedder)
}

func newMemoryIntegrationStoresWithSemanticEmbedder(pool *coredb.Pool, embedder embedding.SemanticEmbedder) (*memoryIntegrationStores, error) {
	var memoryStore *Store
	var err error
	if embedder == nil {
		memoryStore, err = personal.NewStoreFromPool(pool, nil)
	} else {
		memoryStore, err = personal.NewStoreFromPool(pool, embedder)
	}
	if err != nil {
		return nil, err
	}
	historyStore, err := history.NewStoreFromPool(pool)
	if err != nil {
		return nil, err
	}
	compactionStore, err := historycompaction.NewStoreFromPool(pool)
	if err != nil {
		return nil, err
	}
	runtimeStore, err := historyruntime.NewStoreFromPool(pool)
	if err != nil {
		return nil, err
	}
	extractionStore, err := extraction.NewStoreFromPool(pool, embedder)
	if err != nil {
		return nil, err
	}
	knowledgeStore, err := knowledge.NewStoreFromPool(pool, embedder)
	if err != nil {
		return nil, err
	}
	socialStore, err := social.NewStoreFromPool(pool)
	if err != nil {
		return nil, err
	}
	ledgerStore, err := ledger.NewStoreFromPool(pool)
	if err != nil {
		return nil, err
	}
	return &memoryIntegrationStores{
		Store:                 memoryStore,
		integrationHistory:    integrationHistory{Store: historyStore},
		integrationCompaction: integrationCompaction{Store: compactionStore},
		integrationRuntime:    integrationRuntime{Store: runtimeStore},
		integrationExtraction: integrationExtraction{Store: extractionStore},
		integrationKnowledge:  integrationKnowledge{Store: knowledgeStore},
		integrationSocial:     integrationSocial{Store: socialStore},
		integrationLedger:     integrationLedger{Store: ledgerStore},
	}, nil
}

func (s *memoryIntegrationStores) RetrieveContext(ctx context.Context, characterID string, query string) (recall.Context, error) {
	private, err := s.Store.RetrieveContext(ctx, characterID, query)
	if err != nil {
		return recall.Context{}, err
	}
	if ctx.Err() != nil && private.Empty() {
		return recall.Context{PersonalMemories: private.PersonalMemories, SemanticStatus: private.SemanticStatus}, nil
	}
	public, err := s.integrationKnowledge.Store.RetrieveContext(ctx, query)
	if err != nil {
		return recall.Context{}, err
	}
	result := recall.Context{PersonalMemories: private.PersonalMemories, Knowledge: public.Entries, SemanticStatus: private.SemanticStatus}
	if result.SemanticStatus == "" {
		result.SemanticStatus = public.SemanticStatus
	}
	return result, nil
}

func (s *memoryIntegrationStores) RetrievePublicKnowledgeContext(ctx context.Context, query string) (recall.Context, error) {
	public, err := s.integrationKnowledge.Store.RetrieveContext(ctx, query)
	if err != nil {
		return recall.Context{}, err
	}
	return recall.Context{Knowledge: public.Entries, SemanticStatus: public.SemanticStatus}, nil
}

func (s *memoryIntegrationStores) TombstoneKnowledgeContext(ctx context.Context, id string) error {
	return s.integrationKnowledge.Store.TombstoneContext(ctx, id)
}

func (s *memoryIntegrationStores) ConfirmKnowledgeCandidateContext(ctx context.Context, id string) (knowledge.Record, error) {
	return s.integrationKnowledge.Store.ConfirmCandidateContext(ctx, id)
}

func (s *memoryIntegrationStores) ReplaceSemanticEmbedder(embedder embedding.SemanticEmbedder) {
	s.Store.ReplaceSemanticEmbedder(embedder)
	s.integrationKnowledge.Store.ReplaceSemanticEmbedder(embedder)
}

func (s *memoryIntegrationStores) RebuildVectors(ctx context.Context, pageSize int) (VectorRebuildResult, error) {
	result, err := s.Store.RebuildVectors(ctx, pageSize)
	if err != nil {
		return result, err
	}
	public, err := s.integrationKnowledge.Store.RebuildVectors(ctx, pageSize)
	result.ScannedItems += public.ScannedItems
	result.UpdatedItems += public.UpdatedItems
	result.SkippedItems += public.SkippedItems
	result.FailedItems += public.FailedItems
	return result, err
}

type fixedSemanticEmbedder struct {
	ready   bool
	modelID string
	dims    int
	vectors [][]float32
	err     error
	inputs  [][]string
}

func (e *fixedSemanticEmbedder) ModelID() string {
	if e.modelID != "" {
		return e.modelID
	}
	return testSemanticEmbeddingModelID
}

func (e *fixedSemanticEmbedder) Ready() bool { return e.ready }

func (e *fixedSemanticEmbedder) Status() embedding.SemanticStatus {
	if e.ready {
		return embedding.SemanticStatusReady
	}
	return embedding.SemanticStatusUnavailable
}

func (e *fixedSemanticEmbedder) Dims() int { return e.dims }

func (e *fixedSemanticEmbedder) Embed(texts []string) ([][]float32, error) {
	e.inputs = append(e.inputs, append([]string(nil), texts...))
	return e.vectors, e.err
}
