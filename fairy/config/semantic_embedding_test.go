package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSemanticEmbeddingSettingsMissingDefaultsLocal(t *testing.T) {
	root := t.TempDir()
	settings, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatalf("ReadSemanticEmbeddingSettings() error = %v", err)
	}
	if !settings.Enabled {
		t.Fatal("Enabled = false, want true for local default")
	}
	if settings.Provider != SemanticEmbeddingProviderLocalBGE {
		t.Fatalf("Provider = %q, want %q", settings.Provider, SemanticEmbeddingProviderLocalBGE)
	}
	if settings.Model != "" {
		t.Fatalf("Model = %q, want empty", settings.Model)
	}
	if settings.Dimensions != SemanticEmbeddingDimensions {
		t.Fatalf("Dimensions = %d, want %d", settings.Dimensions, SemanticEmbeddingDimensions)
	}
	if _, err := os.Stat(filepath.Join(root, "semantic_embedding")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("semantic_embedding dir stat error = %v, want not exist", err)
	}
}

func TestWriteSemanticEmbeddingSettingsRequiresModelWhenEnabled(t *testing.T) {
	err := WriteSemanticEmbeddingSettings(t.TempDir(), SemanticEmbeddingSettings{Provider: SemanticEmbeddingProviderOpenAICompatible, Dimensions: SemanticEmbeddingDimensions})
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("WriteSemanticEmbeddingSettings() error = %v, want model required", err)
	}
}

func TestWriteSemanticEmbeddingSettingsRequires512Dimensions(t *testing.T) {
	err := WriteSemanticEmbeddingSettings(t.TempDir(), SemanticEmbeddingSettings{Provider: SemanticEmbeddingProviderOpenAICompatible, Model: "text-embedding-3-small", Dimensions: 1536})
	if err == nil || !strings.Contains(err.Error(), "dimensions = 1536, want 512") {
		t.Fatalf("WriteSemanticEmbeddingSettings() error = %v, want dimensions error", err)
	}
}

func TestWriteReadSemanticEmbeddingAPISettings(t *testing.T) {
	root := t.TempDir()
	err := WriteSemanticEmbeddingSettings(root, SemanticEmbeddingSettings{
		Provider: SemanticEmbeddingProviderOpenAICompatible,
		Model:    " text-embedding-3-small ",
	})
	if err != nil {
		t.Fatalf("WriteSemanticEmbeddingSettings() error = %v", err)
	}
	settings, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatalf("ReadSemanticEmbeddingSettings() error = %v", err)
	}
	if !settings.Enabled || settings.Provider != SemanticEmbeddingProviderOpenAICompatible || settings.Model != "text-embedding-3-small" || settings.Dimensions != SemanticEmbeddingDimensions {
		t.Fatalf("settings = %#v", settings)
	}
	status := SemanticEmbeddingStatusFromSettings(settings)
	if !status.Enabled || !status.Configured || status.Provider != settings.Provider || status.Model != settings.Model || status.Dimensions != SemanticEmbeddingDimensions {
		t.Fatalf("status = %#v", status)
	}
}

func TestReadLegacyEnabledSemanticEmbeddingSettingsAsAPIProvider(t *testing.T) {
	root := t.TempDir()
	err := WriteSemanticEmbeddingSettings(root, SemanticEmbeddingSettings{
		Enabled: true,
		Model:   "legacy-embedding",
	})
	if err != nil {
		t.Fatalf("WriteSemanticEmbeddingSettings(legacy) error = %v", err)
	}
	settings, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatalf("ReadSemanticEmbeddingSettings() error = %v", err)
	}
	if settings.Provider != SemanticEmbeddingProviderOpenAICompatible || settings.Model != "legacy-embedding" {
		t.Fatalf("legacy settings = %#v", settings)
	}
}

func TestConfigServiceSemanticEmbeddingStatusAndSave(t *testing.T) {
	service := NewConfigService(t.TempDir(), nil)
	status, err := service.SemanticEmbeddingStatus()
	if err != nil {
		t.Fatalf("SemanticEmbeddingStatus() error = %v", err)
	}
	if !status.Enabled || !status.Configured || status.Provider != SemanticEmbeddingProviderLocalBGE || status.Dimensions != SemanticEmbeddingDimensions {
		t.Fatalf("default status = %#v", status)
	}
	status, err = service.SaveSemanticEmbeddingSettings(SemanticEmbeddingSettings{
		Provider:   SemanticEmbeddingProviderOpenAICompatible,
		Model:      "bge-compatible",
		Dimensions: SemanticEmbeddingDimensions,
	})
	if err != nil {
		t.Fatalf("SaveSemanticEmbeddingSettings() error = %v", err)
	}
	if !status.Enabled || !status.Configured || status.Provider != SemanticEmbeddingProviderOpenAICompatible || status.Model != "bge-compatible" {
		t.Fatalf("saved status = %#v", status)
	}
}
