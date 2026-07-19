package app

import (
	"fairy/companion"
	"fairy/config"
	"fairy/model"
	"go.uber.org/zap"
)

func attachSemanticAPIEmbedder(companionService *companion.CompanionService, modelService *model.ModelService, configReader *config.Reader, logger *zap.Logger) bool {
	if companionService == nil || modelService == nil || configReader == nil {
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
	if !settings.Enabled {
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
}
