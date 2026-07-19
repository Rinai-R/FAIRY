package app

import (
	"testing"

	"fairy/companion"
	"fairy/config"
	"fairy/model"
	"github.com/wailsapp/wails/v3/pkg/application"
)

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

func TestAttachSemanticAPIEmbedderDisabledDoesNothing(t *testing.T) {
	root := t.TempDir()
	service := companion.NewCompanionService()
	modelService := model.NewModelService(root, nil)
	if attached := attachSemanticAPIEmbedder(service, modelService, config.NewReader(root), nil); attached {
		t.Fatal("attachSemanticAPIEmbedder() = true, want false for missing settings")
	}
}

func TestAttachSemanticAPIEmbedderEnabledWithoutModelConnectionDoesNothing(t *testing.T) {
	root := t.TempDir()
	if err := config.WriteSemanticEmbeddingSettings(root, config.SemanticEmbeddingSettings{
		Enabled:    true,
		Model:      "text-embedding-3-small",
		Dimensions: config.SemanticEmbeddingDimensions,
	}); err != nil {
		t.Fatalf("WriteSemanticEmbeddingSettings() error = %v", err)
	}
	service := companion.NewCompanionService()
	modelService := model.NewModelService(root, nil)
	if attached := attachSemanticAPIEmbedder(service, modelService, config.NewReader(root), nil); attached {
		t.Fatal("attachSemanticAPIEmbedder() = true, want false without model connection")
	}
}
