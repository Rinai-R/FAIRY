package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	semanticEmbeddingSettingsFile             = "settings.json"
	SemanticEmbeddingDimensions               = 512
	SemanticEmbeddingProviderNone             = "none"
	SemanticEmbeddingProviderOpenAICompatible = "openai_compatible_api"
	// SemanticEmbeddingProviderLocalBGE is retained only so legacy settings files
	// can be detected and normalized away; it is no longer a supported runtime.
	SemanticEmbeddingProviderLocalBGE = "local_bge_small_zh"
)

type SemanticEmbeddingSettings struct {
	SchemaVersion uint32 `json:"schema_version"`
	Provider      string `json:"provider,omitempty"`
	// Enabled is retained for backward compatibility with older settings shapes.
	Enabled    bool   `json:"enabled"`
	Model      string `json:"model,omitempty"`
	Dimensions int    `json:"dimensions"`
}

type SemanticEmbeddingStatus struct {
	Provider   string `json:"provider"`
	Enabled    bool   `json:"enabled"`
	Model      string `json:"model,omitempty"`
	Dimensions int    `json:"dimensions"`
	Configured bool   `json:"configured"`
}

type semanticEmbeddingDocument struct {
	SchemaVersion uint32                    `json:"schema_version"`
	Data          SemanticEmbeddingSettings `json:"data"`
}

func semanticEmbeddingDir(root string) string {
	return filepath.Join(root, "semantic_embedding")
}

func defaultSemanticEmbeddingSettings() SemanticEmbeddingSettings {
	return SemanticEmbeddingSettings{
		SchemaVersion: 1,
		Provider:      SemanticEmbeddingProviderNone,
		Enabled:       false,
		Dimensions:    SemanticEmbeddingDimensions,
	}
}

func ReadSemanticEmbeddingSettings(root string) (SemanticEmbeddingSettings, error) {
	if root == "" {
		return SemanticEmbeddingSettings{}, errors.New("config root is required")
	}
	path := filepath.Join(semanticEmbeddingDir(root), semanticEmbeddingSettingsFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultSemanticEmbeddingSettings(), nil
		}
		return SemanticEmbeddingSettings{}, fmt.Errorf("reading semantic embedding settings: %w", err)
	}
	var doc semanticEmbeddingDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return SemanticEmbeddingSettings{}, fmt.Errorf("parsing semantic embedding settings: %w", err)
	}
	settings := doc.Data
	if settings.SchemaVersion == 0 {
		settings.SchemaVersion = doc.SchemaVersion
	}
	return normalizeSemanticEmbeddingSettings(settings)
}

func WriteSemanticEmbeddingSettings(root string, settings SemanticEmbeddingSettings) error {
	if root == "" {
		return errors.New("config root is required")
	}
	normalized, err := normalizeSemanticEmbeddingSettings(settings)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(semanticEmbeddingDir(root), 0o755); err != nil {
		return fmt.Errorf("creating semantic embedding settings dir: %w", err)
	}
	doc := semanticEmbeddingDocument{
		SchemaVersion: 1,
		Data:          normalized,
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(semanticEmbeddingDir(root), semanticEmbeddingSettingsFile), raw, 0o600)
}

func SemanticEmbeddingStatusFromSettings(settings SemanticEmbeddingSettings) SemanticEmbeddingStatus {
	settings, err := normalizeSemanticEmbeddingSettings(settings)
	if err != nil {
		return SemanticEmbeddingStatus{Enabled: false, Dimensions: SemanticEmbeddingDimensions, Configured: false}
	}
	return SemanticEmbeddingStatus{
		Provider:   settings.Provider,
		Enabled:    settings.Enabled,
		Model:      settings.Model,
		Dimensions: settings.Dimensions,
		Configured: settings.Provider == SemanticEmbeddingProviderOpenAICompatible && settings.Model != "",
	}
}

func normalizeSemanticEmbeddingSettings(settings SemanticEmbeddingSettings) (SemanticEmbeddingSettings, error) {
	if settings.SchemaVersion == 0 {
		settings.SchemaVersion = 1
	}
	if settings.SchemaVersion != 1 {
		return SemanticEmbeddingSettings{}, fmt.Errorf("semantic embedding settings schema_version = %d, want 1", settings.SchemaVersion)
	}
	settings.Provider = strings.TrimSpace(settings.Provider)
	if settings.Provider == SemanticEmbeddingProviderLocalBGE {
		// Local ONNX/BGE was removed; treat legacy files as unconfigured.
		settings.Provider = SemanticEmbeddingProviderNone
		settings.Enabled = false
		settings.Model = ""
	}
	if settings.Provider == "" {
		if settings.Enabled {
			settings.Provider = SemanticEmbeddingProviderOpenAICompatible
		} else {
			settings.Provider = SemanticEmbeddingProviderNone
		}
	}
	if settings.Provider != SemanticEmbeddingProviderNone && settings.Provider != SemanticEmbeddingProviderOpenAICompatible {
		return SemanticEmbeddingSettings{}, fmt.Errorf("semantic embedding provider %q is not supported", settings.Provider)
	}
	settings.Model = strings.TrimSpace(settings.Model)
	if settings.Dimensions == 0 {
		settings.Dimensions = SemanticEmbeddingDimensions
	}
	if settings.Dimensions != SemanticEmbeddingDimensions {
		return SemanticEmbeddingSettings{}, fmt.Errorf("semantic embedding dimensions = %d, want %d", settings.Dimensions, SemanticEmbeddingDimensions)
	}
	if settings.Provider == SemanticEmbeddingProviderNone {
		settings.Enabled = false
		settings.Model = ""
		return settings, nil
	}
	settings.Enabled = true
	if settings.Model == "" {
		return SemanticEmbeddingSettings{}, errors.New("semantic embedding model is required when openai_compatible_api is enabled")
	}
	return settings, nil
}
