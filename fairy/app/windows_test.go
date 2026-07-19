package app

import (
	"os"
	"testing"
	"testing/fstest"

	"fairy/companion"
	"fairy/config"
	"fairy/memory"
	"fairy/memory/semantic"
	"fairy/model"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type appLocalEmbeddingRunner struct {
	ready bool
	dims  int
}

func (r appLocalEmbeddingRunner) Ready() bool { return r.ready }
func (r appLocalEmbeddingRunner) Dims() int   { return r.dims }

func (r appLocalEmbeddingRunner) Embed(texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for index := range texts {
		vector := make([]float32, r.dims)
		if len(vector) > 0 {
			vector[0] = 1
		}
		vectors[index] = vector
	}
	return vectors, nil
}

func TestMacCurrentSpaceCollectionBehaviorDoesNotJoinAllSpaces(t *testing.T) {
	if macCurrentSpaceCollectionBehavior&application.MacWindowCollectionBehaviorCanJoinAllSpaces != 0 {
		t.Fatal("product windows must stay on the current macOS Space, not join all Spaces")
	}
	if macCurrentSpaceCollectionBehavior&application.MacWindowCollectionBehaviorMoveToActiveSpace != 0 {
		t.Fatal("product windows must not move to whichever macOS Space becomes active")
	}
	if macCurrentSpaceCollectionBehavior&application.MacWindowCollectionBehaviorManaged == 0 {
		t.Fatal("product windows should use managed current-Space behavior")
	}
	if macCurrentSpaceCollectionBehavior&application.MacWindowCollectionBehaviorFullScreenNone == 0 {
		t.Fatal("product windows should not create or follow fullscreen Spaces")
	}
}

func TestAttachSemanticProviderDefaultLocalUnavailableDoesNothing(t *testing.T) {
	root := t.TempDir()
	service := companion.NewCompanionService()
	modelService := model.NewModelService(root, nil)
	if attached := attachSemanticProvider(service, modelService, config.NewReader(root), nil, nil); attached {
		t.Fatal("attachSemanticProvider() = true, want false for missing local resources")
	}
}

func TestAttachSemanticProviderDefaultLocalReadyAttaches(t *testing.T) {
	root := t.TempDir()
	service := companion.NewCompanionService()
	modelService := model.NewModelService(root, nil)
	called := false
	localFactory := func(root string) (semantic.Embedder, error) {
		called = true
		return memory.NewLocalBGEEmbedder(appLocalEmbeddingRunner{ready: true, dims: memory.SemanticEmbeddingDimensions})
	}
	if attached := attachSemanticProvider(service, modelService, config.NewReader(root), nil, localFactory); !attached {
		t.Fatal("attachSemanticProvider() = false, want true for ready local provider")
	}
	if !called {
		t.Fatal("local provider factory was not called")
	}
}

func TestPrepareAndAttachSemanticProviderPreparesBundledAssetsBeforeLocalFactory(t *testing.T) {
	root := t.TempDir()
	service := companion.NewCompanionService()
	modelService := model.NewModelService(root, nil)
	bundle := fstest.MapFS{
		memory.SemanticEmbeddingBundleModelRelativePath:     {Data: []byte("model"), Mode: 0o600},
		memory.SemanticEmbeddingBundleModelDataRelativePath: {Data: []byte("model-data"), Mode: 0o600},
		memory.SemanticEmbeddingBundleTokenizerRelativePath: {Data: []byte("tokenizer"), Mode: 0o600},
		memory.SemanticEmbeddingBundleRuntimeRelativePath(): {Data: []byte("runtime"), Mode: 0o600},
	}
	preparer := func(root string) (memory.SemanticEmbeddingAssetPreparation, error) {
		return memory.PrepareBundledSemanticEmbeddingAssets(root, bundle)
	}
	localFactory := func(root string) (semantic.Embedder, error) {
		modelPath, err := memory.SemanticEmbeddingModelPath(root)
		if err != nil {
			return nil, err
		}
		modelDataPath, err := memory.SemanticEmbeddingModelDataPath(root)
		if err != nil {
			return nil, err
		}
		tokenizerPath, err := memory.SemanticEmbeddingTokenizerPath(root)
		if err != nil {
			return nil, err
		}
		runtimePath, err := memory.DefaultSemanticEmbeddingRuntimeLibraryPath(root)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(modelPath); err != nil {
			t.Fatalf("local factory ran before model asset was prepared: %v", err)
		}
		if _, err := os.Stat(modelDataPath); err != nil {
			t.Fatalf("local factory ran before model data asset was prepared: %v", err)
		}
		if _, err := os.Stat(tokenizerPath); err != nil {
			t.Fatalf("local factory ran before tokenizer asset was prepared: %v", err)
		}
		if _, err := os.Stat(runtimePath); err != nil {
			t.Fatalf("local factory ran before runtime asset was prepared: %v", err)
		}
		return memory.NewLocalBGEEmbedder(appLocalEmbeddingRunner{ready: true, dims: memory.SemanticEmbeddingDimensions})
	}
	if attached := prepareAndAttachSemanticProvider(service, modelService, config.NewReader(root), nil, preparer, localFactory); !attached {
		t.Fatal("prepareAndAttachSemanticProvider() = false, want true after bundled assets are prepared")
	}
}

func TestAttachSemanticProviderAPISelectedWithoutModelConnectionDoesNothing(t *testing.T) {
	root := t.TempDir()
	if err := config.WriteSemanticEmbeddingSettings(root, config.SemanticEmbeddingSettings{
		Provider:   config.SemanticEmbeddingProviderOpenAICompatible,
		Model:      "text-embedding-3-small",
		Dimensions: config.SemanticEmbeddingDimensions,
	}); err != nil {
		t.Fatalf("WriteSemanticEmbeddingSettings() error = %v", err)
	}
	service := companion.NewCompanionService()
	modelService := model.NewModelService(root, nil)
	if attached := attachSemanticProvider(service, modelService, config.NewReader(root), nil, nil); attached {
		t.Fatal("attachSemanticProvider() = true, want false without model connection")
	}
}

func TestAttachSemanticProviderAPISelectedAttachesWhenConfigured(t *testing.T) {
	root := t.TempDir()
	if _, err := config.SaveModelConnection(root, config.ModelConnectionInput{
		Protocol:            "chat_completions",
		Endpoint:            "http://127.0.0.1:1",
		Model:               "chat-model",
		ContextWindowTokens: 8192,
		AuthMode:            "no_auth",
	}, nil, nil); err != nil {
		t.Fatalf("SaveModelConnection() error = %v", err)
	}
	if err := config.WriteSemanticEmbeddingSettings(root, config.SemanticEmbeddingSettings{
		Provider:   config.SemanticEmbeddingProviderOpenAICompatible,
		Model:      "text-embedding-3-small",
		Dimensions: config.SemanticEmbeddingDimensions,
	}); err != nil {
		t.Fatalf("WriteSemanticEmbeddingSettings() error = %v", err)
	}
	service := companion.NewCompanionService()
	modelService := model.NewModelService(root, nil)
	if attached := attachSemanticProvider(service, modelService, config.NewReader(root), nil, nil); !attached {
		t.Fatal("attachSemanticProvider() = false, want true for configured API provider")
	}
}
