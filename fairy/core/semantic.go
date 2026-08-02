package core

import (
	"fairy/config"
	"fairy/memory"
	"fairy/model"

	"go.uber.org/zap"
)

func semanticEmbedder(modelService *model.ModelService, configReader *config.Reader, logger *zap.Logger) memory.SemanticEmbedder {
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
