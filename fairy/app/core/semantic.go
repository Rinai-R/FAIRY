package core

import (
	"fairy/context/memory/personal"
	"fairy/runtime/config"
	"fairy/runtime/embedding"
	"fairy/runtime/model"

	"go.uber.org/zap"
)

func modelServiceForProfile(profile Profile, root string, secrets *config.SecretStore) *model.ModelService {
	if profile == ProfileEndpointStrict {
		return model.NewEndpointModelService(root, secrets)
	}
	return model.NewModelService(root, secrets)
}

func semanticEmbedder(modelService *model.ModelService, configReader *config.Reader, logger *zap.Logger) embedding.SemanticEmbedder {
	if configReader == nil {
		return nil
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	settings, err := configReader.SemanticEmbeddingSettings()
	if err != nil {
		logger.Warn("semantic embedding settings unavailable", zap.Error(err))
		return nil
	}
	if !settings.Enabled || settings.LegacyReason != "" {
		logger.Info("semantic embedding disabled; FTS-only retrieval")
		return nil
	}
	switch settings.Provider {
	case config.SemanticEmbeddingProviderNone, "":
		logger.Info("semantic embedding disabled; FTS-only retrieval")
		return nil
	case config.SemanticEmbeddingProviderSiliconFlow, config.SemanticEmbeddingProviderOpenAICompatible:
		if modelService == nil {
			return nil
		}
		embedder, err := modelService.SemanticEmbedder(settings)
		if err != nil {
			logger.Warn("semantic API embedder unavailable", zap.Error(err))
			return nil
		}
		logger.Info("semantic API embedder attached", zap.String("model", settings.Model), zap.Int("dimensions", settings.Dimensions))
		return embedder
	default:
		logger.Warn("semantic embedding provider unsupported", zap.String("provider", settings.Provider))
		return nil
	}
}

type semanticEmbeddingRuntime struct {
	model *model.ModelService
	store semanticEmbedderPublisher
}

type semanticEmbedderPublisher interface {
	ReplaceSemanticEmbedder(embedding.SemanticEmbedder)
}

type semanticEmbedderPublishers []semanticEmbedderPublisher

func (publishers semanticEmbedderPublishers) ReplaceSemanticEmbedder(embedder embedding.SemanticEmbedder) {
	for _, publisher := range publishers {
		if publisher != nil {
			publisher.ReplaceSemanticEmbedder(embedder)
		}
	}
}

func (runtime semanticEmbeddingRuntime) PrepareSemanticEmbedding(settings config.SemanticEmbeddingSettings) (func(), error) {
	if runtime.store == nil {
		return nil, personal.ErrStoreBackendUnavailable
	}
	if !settings.Enabled || settings.LegacyReason != "" || settings.Provider == config.SemanticEmbeddingProviderNone {
		return func() { runtime.store.ReplaceSemanticEmbedder(nil) }, nil
	}
	if runtime.model == nil {
		return nil, embedding.ErrSemanticUnavailable
	}
	embedder, err := runtime.model.SemanticEmbedder(settings)
	if err != nil {
		return nil, err
	}
	return func() { runtime.store.ReplaceSemanticEmbedder(embedder) }, nil
}

func (runtime semanticEmbeddingRuntime) DisableSemanticEmbedding() {
	if runtime.store != nil {
		runtime.store.ReplaceSemanticEmbedder(nil)
	}
}
