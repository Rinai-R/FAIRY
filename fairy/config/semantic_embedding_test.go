package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSemanticEmbeddingSettingsMissingDefaultsDisabled(t *testing.T) {
	root := t.TempDir()
	settings, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatalf("ReadSemanticEmbeddingSettings() error = %v", err)
	}
	if settings.Enabled {
		t.Fatal("Enabled = true, want false")
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
	err := WriteSemanticEmbeddingSettings(t.TempDir(), SemanticEmbeddingSettings{Enabled: true, Dimensions: SemanticEmbeddingDimensions})
	if err == nil || !strings.Contains(err.Error(), "model is required") {
		t.Fatalf("WriteSemanticEmbeddingSettings() error = %v, want model required", err)
	}
}

func TestWriteSemanticEmbeddingSettingsRequires512Dimensions(t *testing.T) {
	err := WriteSemanticEmbeddingSettings(t.TempDir(), SemanticEmbeddingSettings{Enabled: true, Model: "text-embedding-3-small", Dimensions: 1536})
	if err == nil || !strings.Contains(err.Error(), "dimensions = 1536, want 512") {
		t.Fatalf("WriteSemanticEmbeddingSettings() error = %v, want dimensions error", err)
	}
}

func TestWriteReadSemanticEmbeddingSettings(t *testing.T) {
	root := t.TempDir()
	err := WriteSemanticEmbeddingSettings(root, SemanticEmbeddingSettings{
		Enabled: true,
		Model:   " text-embedding-3-small ",
	})
	if err != nil {
		t.Fatalf("WriteSemanticEmbeddingSettings() error = %v", err)
	}
	settings, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatalf("ReadSemanticEmbeddingSettings() error = %v", err)
	}
	if !settings.Enabled || settings.Model != "text-embedding-3-small" || settings.Dimensions != SemanticEmbeddingDimensions {
		t.Fatalf("settings = %#v", settings)
	}
	status := SemanticEmbeddingStatusFromSettings(settings)
	if !status.Enabled || !status.Configured || status.Model != settings.Model || status.Dimensions != SemanticEmbeddingDimensions {
		t.Fatalf("status = %#v", status)
	}
}

func TestConfigServiceSemanticEmbeddingStatusAndSave(t *testing.T) {
	service := NewConfigService(t.TempDir(), nil)
	status, err := service.SemanticEmbeddingStatus()
	if err != nil {
		t.Fatalf("SemanticEmbeddingStatus() error = %v", err)
	}
	if status.Enabled || status.Configured || status.Dimensions != SemanticEmbeddingDimensions {
		t.Fatalf("default status = %#v", status)
	}
	status, err = service.SaveSemanticEmbeddingSettings(SemanticEmbeddingSettings{
		Enabled:    true,
		Model:      "bge-compatible",
		Dimensions: SemanticEmbeddingDimensions,
	})
	if err != nil {
		t.Fatalf("SaveSemanticEmbeddingSettings() error = %v", err)
	}
	if !status.Enabled || !status.Configured || status.Model != "bge-compatible" {
		t.Fatalf("saved status = %#v", status)
	}
}
