package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	semanticEmbeddingSettingsFile = "settings.json"

	SemanticEmbeddingDimensions       = 1024
	SemanticEmbeddingLegacyDimensions = 512

	SemanticEmbeddingProviderNone             = "none"
	SemanticEmbeddingProviderSiliconFlow      = "siliconflow"
	SemanticEmbeddingProviderOpenAICompatible = "openai_compatible_api"
	// SemanticEmbeddingProviderLocalBGE is retained only so legacy settings files
	// can be detected and normalized away; it is no longer a supported runtime.
	SemanticEmbeddingProviderLocalBGE = "local_bge_small_zh"

	semanticEmbeddingSchemaVersion    = 2
	semanticEmbeddingSiliconFlowURL   = "https://api.siliconflow.cn/v1"
	semanticEmbeddingSiliconFlowModel = "BAAI/bge-m3"
)

type SemanticEmbeddingSettings struct {
	SchemaVersion uint32 `json:"schema_version"`
	Provider      string `json:"provider,omitempty"`
	// Enabled is retained for backward compatibility with older settings shapes.
	Enabled      bool   `json:"enabled"`
	Endpoint     string `json:"endpoint,omitempty"`
	Model        string `json:"model,omitempty"`
	Dimensions   int    `json:"dimensions"`
	ConnectionID string `json:"connection_id,omitempty"`
	LegacyReason string `json:"-"`
}

type SemanticEmbeddingStatus struct {
	Provider             string `json:"provider"`
	Enabled              bool   `json:"enabled"`
	Endpoint             string `json:"endpoint,omitempty"`
	Model                string `json:"model,omitempty"`
	Dimensions           int    `json:"dimensions"`
	Configured           bool   `json:"configured"`
	CredentialConfigured bool   `json:"credentialConfigured"`
	Reason               string `json:"reason,omitempty"`
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
		SchemaVersion: semanticEmbeddingSchemaVersion,
		Provider:      SemanticEmbeddingProviderNone,
		Dimensions:    SemanticEmbeddingDimensions,
	}
}

func ReadSemanticEmbeddingSettings(root string) (SemanticEmbeddingSettings, error) {
	if root == "" {
		return SemanticEmbeddingSettings{}, errors.New("config root is required")
	}
	filename := filepath.Join(semanticEmbeddingDir(root), semanticEmbeddingSettingsFile)
	raw, err := os.ReadFile(filename)
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
	if normalized.LegacyReason != "" {
		return errors.New(normalized.LegacyReason)
	}
	dir := semanticEmbeddingDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating semantic embedding settings dir: %w", err)
	}
	doc := semanticEmbeddingDocument{SchemaVersion: semanticEmbeddingSchemaVersion, Data: normalized}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	temporary, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary semantic embedding settings: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("securing temporary semantic embedding settings: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return fmt.Errorf("writing temporary semantic embedding settings: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("syncing temporary semantic embedding settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing temporary semantic embedding settings: %w", err)
	}
	if err := os.Rename(temporaryName, filepath.Join(dir, semanticEmbeddingSettingsFile)); err != nil {
		return fmt.Errorf("replacing semantic embedding settings: %w", err)
	}
	return nil
}

func SemanticEmbeddingStatusFromSettings(settings SemanticEmbeddingSettings) SemanticEmbeddingStatus {
	settings, err := normalizeSemanticEmbeddingSettings(settings)
	if err != nil {
		return SemanticEmbeddingStatus{Dimensions: SemanticEmbeddingDimensions, Reason: err.Error()}
	}
	status := SemanticEmbeddingStatus{
		Provider:   settings.Provider,
		Enabled:    settings.Enabled,
		Endpoint:   settings.Endpoint,
		Model:      settings.Model,
		Dimensions: settings.Dimensions,
		Reason:     settings.LegacyReason,
	}
	status.Configured = settings.Enabled && settings.LegacyReason == "" && settings.Provider != SemanticEmbeddingProviderNone && settings.Endpoint != "" && settings.Model != ""
	if settings.Provider != SemanticEmbeddingProviderNone && !settings.Enabled && settings.LegacyReason == "" {
		status.Reason = "semantic_embedding_disabled"
	}
	return status
}

func normalizeSemanticEmbeddingSettings(settings SemanticEmbeddingSettings) (SemanticEmbeddingSettings, error) {
	if settings.SchemaVersion == 0 {
		settings.SchemaVersion = 1
	}
	legacy := settings.SchemaVersion == 1 || (settings.SchemaVersion == semanticEmbeddingSchemaVersion && settings.Provider == SemanticEmbeddingProviderOpenAICompatible && settings.Dimensions == SemanticEmbeddingLegacyDimensions)
	if !legacy && settings.SchemaVersion != semanticEmbeddingSchemaVersion {
		return SemanticEmbeddingSettings{}, fmt.Errorf("semantic embedding settings schema_version = %d, want 1 or %d", settings.SchemaVersion, semanticEmbeddingSchemaVersion)
	}
	settings.Provider = strings.TrimSpace(settings.Provider)
	settings.Endpoint = strings.TrimSpace(settings.Endpoint)
	settings.Model = strings.TrimSpace(settings.Model)
	if settings.Provider == SemanticEmbeddingProviderLocalBGE {
		settings.Provider = SemanticEmbeddingProviderNone
		settings.Enabled = false
		settings.Endpoint = ""
		settings.Model = ""
		settings.Dimensions = SemanticEmbeddingDimensions
		settings.SchemaVersion = semanticEmbeddingSchemaVersion
		return settings, nil
	}
	if settings.Provider == "" {
		if settings.Enabled {
			settings.Provider = SemanticEmbeddingProviderOpenAICompatible
		} else {
			settings.Provider = SemanticEmbeddingProviderNone
		}
	}
	if settings.Dimensions == 0 {
		if legacy && settings.Provider == SemanticEmbeddingProviderOpenAICompatible {
			settings.Dimensions = SemanticEmbeddingLegacyDimensions
		} else {
			settings.Dimensions = SemanticEmbeddingDimensions
		}
	}
	if settings.Provider == SemanticEmbeddingProviderNone {
		settings.Enabled = false
		settings.Endpoint = ""
		settings.Model = ""
		settings.Dimensions = SemanticEmbeddingDimensions
		settings.SchemaVersion = semanticEmbeddingSchemaVersion
		return settings, nil
	}
	if settings.Provider != SemanticEmbeddingProviderSiliconFlow && settings.Provider != SemanticEmbeddingProviderOpenAICompatible {
		return SemanticEmbeddingSettings{}, fmt.Errorf("semantic embedding provider %q is not supported", settings.Provider)
	}
	if settings.Provider == SemanticEmbeddingProviderSiliconFlow {
		if settings.Endpoint == "" {
			settings.Endpoint = semanticEmbeddingSiliconFlowURL
		}
		if settings.Model == "" {
			settings.Model = semanticEmbeddingSiliconFlowModel
		}
		if settings.Model != semanticEmbeddingSiliconFlowModel {
			return SemanticEmbeddingSettings{}, fmt.Errorf("siliconflow semantic embedding model %q is not supported, want %q", settings.Model, semanticEmbeddingSiliconFlowModel)
		}
	}
	if settings.Endpoint == "" {
		if legacy {
			settings.LegacyReason = "legacy_512_reconfiguration_required"
		} else {
			return SemanticEmbeddingSettings{}, errors.New("semantic embedding endpoint is required")
		}
	} else if err := validateSemanticEmbeddingEndpoint(settings.Endpoint); err != nil {
		return SemanticEmbeddingSettings{}, err
	}
	if settings.Model == "" {
		if legacy {
			settings.LegacyReason = "legacy_512_reconfiguration_required"
		} else {
			return SemanticEmbeddingSettings{}, errors.New("semantic embedding model is required")
		}
	}
	if settings.Dimensions != SemanticEmbeddingDimensions {
		if legacy && settings.Dimensions == SemanticEmbeddingLegacyDimensions {
			settings.LegacyReason = "legacy_512_reconfiguration_required"
		} else {
			return SemanticEmbeddingSettings{}, fmt.Errorf("semantic embedding dimensions = %d, want %d", settings.Dimensions, SemanticEmbeddingDimensions)
		}
	}
	if settings.LegacyReason != "" {
		settings.Enabled = false
	} else if legacy {
		settings.LegacyReason = "legacy_512_reconfiguration_required"
		settings.Enabled = false
	}
	settings.SchemaVersion = semanticEmbeddingSchemaVersion
	return settings, nil
}

func validateSemanticEmbeddingEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("semantic embedding endpoint must be an HTTP(S) base URL without userinfo, query or fragment")
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	last := ""
	if len(segments) > 0 {
		last = segments[len(segments)-1]
	}
	if last == "responses" || last == "embeddings" || (len(segments) >= 2 && segments[len(segments)-2] == "chat" && last == "completions") {
		return errors.New("semantic embedding endpoint must be a base URL, not a protocol resource URL")
	}
	return nil
}

func SiliconFlowSemanticEmbeddingDefaults() SemanticEmbeddingSettings {
	return SemanticEmbeddingSettings{
		SchemaVersion: semanticEmbeddingSchemaVersion,
		Provider:      SemanticEmbeddingProviderSiliconFlow,
		Enabled:       true,
		Endpoint:      semanticEmbeddingSiliconFlowURL,
		Model:         semanticEmbeddingSiliconFlowModel,
		Dimensions:    SemanticEmbeddingDimensions,
	}
}
