package runtime

import (
	"fairy/companion"
	"fairy/config"
	"fairy/model"
	"go.uber.org/zap"
)

func attachSemanticEmbedder(companionService *companion.CompanionService, modelService *model.ModelService, configReader *config.Reader, logger *zap.Logger) bool {
	if companionService == nil || configReader == nil {
		return false
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	settings, err := configReader.SemanticEmbeddingSettings()
	if err != nil {
		logger.Warn("semantic embedding settings unavailable", zap.Error(err))
		return false
	}
	switch settings.Provider {
	case config.SemanticEmbeddingProviderNone, "":
		logger.Info("semantic embedding disabled; FTS-only retrieval")
		return false
	case config.SemanticEmbeddingProviderOpenAICompatible:
		if modelService == nil {
			return false
		}
		embedder, err := modelService.SemanticAPIEmbedder(settings)
		if err != nil {
			logger.Warn("semantic API embedder unavailable", zap.Error(err))
			return false
		}
		companion.AttachSemanticEmbedder(companionService, embedder)
		logger.Info("semantic API embedder attached", zap.String("model", settings.Model), zap.Int("dimensions", settings.Dimensions))
		return true
	default:
		logger.Warn("semantic embedding provider unsupported", zap.String("provider", settings.Provider))
		return false
	}
}
