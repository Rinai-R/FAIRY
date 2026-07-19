package app

import (
	"io/fs"

	"fairy/companion"
	"fairy/config"
	"fairy/memory"
	"fairy/memory/semantic"
	"fairy/model"
	"go.uber.org/zap"
)

type localSemanticEmbedderFactory func(root string) (semantic.Embedder, error)
type semanticAssetPreparer func(root string) (memory.SemanticEmbeddingAssetPreparation, error)

func attachSemanticEmbedder(companionService *companion.CompanionService, modelService *model.ModelService, configReader *config.Reader, bundle fs.FS, logger *zap.Logger) bool {
	preparer := func(root string) (memory.SemanticEmbeddingAssetPreparation, error) {
		return memory.PrepareBundledSemanticEmbeddingAssets(root, bundle)
	}
	return prepareAndAttachSemanticProvider(companionService, modelService, configReader, logger, preparer, defaultLocalSemanticEmbedder)
}

func prepareAndAttachSemanticProvider(companionService *companion.CompanionService, modelService *model.ModelService, configReader *config.Reader, logger *zap.Logger, preparer semanticAssetPreparer, localFactory localSemanticEmbedderFactory) bool {
	if logger == nil {
		logger = zap.NewNop()
	}
	if configReader != nil && preparer != nil {
		result, err := preparer(configReader.Root())
		if err != nil {
			logger.Warn("semantic embedding assets preparation failed", zap.Error(err))
		} else if result.Copied() {
			logger.Info("semantic embedding assets prepared", zap.Bool("modelCopied", result.ModelCopied), zap.Bool("modelDataCopied", result.ModelDataCopied), zap.Bool("tokenizerCopied", result.TokenizerCopied), zap.Bool("runtimeCopied", result.RuntimeCopied))
		} else if !result.Ready() {
			logger.Debug("semantic embedding bundled assets unavailable", zap.String("reason", result.Reason))
		}
	}
	return attachSemanticProvider(companionService, modelService, configReader, logger, localFactory)
}

func attachSemanticProvider(companionService *companion.CompanionService, modelService *model.ModelService, configReader *config.Reader, logger *zap.Logger, localFactory localSemanticEmbedderFactory) bool {
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
	case config.SemanticEmbeddingProviderLocalBGE:
		if localFactory == nil {
			localFactory = defaultLocalSemanticEmbedder
		}
		embedder, err := localFactory(configReader.Root())
		if err != nil {
			logger.Warn("local semantic embedder unavailable", zap.Error(err))
			return false
		}
		if embedder == nil || !embedder.Ready() {
			logger.Warn("local semantic embedder is not ready")
			return false
		}
		companion.AttachSemanticEmbedder(companionService, embedder)
		logger.Info("local semantic embedder attached", zap.Int("dimensions", embedder.Dims()))
		return true
	case config.SemanticEmbeddingProviderOpenAICompatible:
		return attachSemanticAPIEmbedderWithSettings(companionService, modelService, settings, logger)
	default:
		logger.Warn("semantic embedding provider unsupported", zap.String("provider", settings.Provider))
		return false
	}
}

func defaultLocalSemanticEmbedder(root string) (semantic.Embedder, error) {
	runner, err := memory.NewLocalONNXEmbeddingRunner(root)
	if err != nil {
		return nil, err
	}
	return memory.NewLocalBGEEmbedder(runner)
}

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
	return attachSemanticAPIEmbedderWithSettings(companionService, modelService, settings, logger)
}

func attachSemanticAPIEmbedderWithSettings(companionService *companion.CompanionService, modelService *model.ModelService, settings config.SemanticEmbeddingSettings, logger *zap.Logger) bool {
	if companionService == nil || modelService == nil {
		return false
	}
	if logger == nil {
		logger = zap.NewNop()
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
