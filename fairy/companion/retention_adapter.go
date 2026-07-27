package companion

import (
	"context"

	"fairy/character"
	"fairy/config"
	"fairy/memory/semantic"
	"fairy/model"
	"fairy/retention"
)

type retentionHost struct{ service *CompanionService }

var _ retention.Host = retentionHost{}

func (h retentionHost) ExtractionStore() retention.ExtractionStore {
	if h.service == nil {
		return nil
	}
	return h.service.memory.retention.extraction
}

func (h retentionHost) KnowledgeIngestStore() retention.KnowledgeIngestStore {
	if h.service == nil {
		return nil
	}
	return h.service.memory.retention.knowledge
}

func (h retentionHost) ActiveCharacter(characterID string) (character.Record, error) {
	if h.service == nil {
		return character.Record{}, ErrRespondRuntimeNotMigrated
	}
	return h.service.activeCharacter(characterID)
}

func (h retentionHost) ModelConnection() (config.ModelConnection, error) {
	if h.service == nil || h.service.configSource() == nil {
		return config.ModelConnection{}, ErrRespondRuntimeNotMigrated
	}
	return h.service.configSource().ModelConnection()
}

func (h retentionHost) ExecuteModel(ctx context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	if h.service == nil || h.service.modelPort() == nil {
		return nil, ErrRespondRuntimeNotMigrated
	}
	return h.service.modelPort().ExecuteRequestContext(ctx, request)
}

func (h retentionHost) SemanticEmbedder() semantic.Embedder {
	if h.service == nil {
		return nil
	}
	return h.service.semanticEmbedder
}

func (h retentionHost) VectorIndex() retention.VectorIndex {
	if h.service == nil {
		return nil
	}
	return h.service.vectorIndex
}

func (h retentionHost) SetBackgroundError(err error) {
	if h.service != nil {
		h.service.setBackgroundError(err)
	}
}

func (h retentionHost) ClearBackgroundError() {
	if h.service != nil {
		h.service.clearBackgroundError()
	}
}
