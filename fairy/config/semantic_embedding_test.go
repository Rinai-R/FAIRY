package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSemanticEmbeddingSettingsMissingDefaultsNone(t *testing.T) {
	root := t.TempDir()
	settings, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatalf("ReadSemanticEmbeddingSettings() error = %v", err)
	}
	if settings.Enabled || settings.Provider != SemanticEmbeddingProviderNone {
		t.Fatalf("settings = %#v, want disabled none provider", settings)
	}
	if settings.SchemaVersion != semanticEmbeddingSchemaVersion || settings.Dimensions != SemanticEmbeddingDimensions {
		t.Fatalf("settings = %#v, want schema v2 and %d dimensions", settings, SemanticEmbeddingDimensions)
	}
	if _, err := os.Stat(filepath.Join(root, "semantic_embedding")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("semantic_embedding dir stat error = %v, want not exist", err)
	}
}

func TestWriteSemanticEmbeddingSettingsRequiresEndpointForGenericAPI(t *testing.T) {
	err := WriteSemanticEmbeddingSettings(t.TempDir(), SemanticEmbeddingSettings{
		SchemaVersion: semanticEmbeddingSchemaVersion,
		Provider:      SemanticEmbeddingProviderOpenAICompatible,
		Model:         "embedding-model",
		Dimensions:    SemanticEmbeddingDimensions,
	})
	if err == nil || !strings.Contains(err.Error(), "endpoint is required") {
		t.Fatalf("WriteSemanticEmbeddingSettings() error = %v, want endpoint required", err)
	}
}

func TestWriteSemanticEmbeddingSettingsRequires1024Dimensions(t *testing.T) {
	err := WriteSemanticEmbeddingSettings(t.TempDir(), SemanticEmbeddingSettings{
		SchemaVersion: semanticEmbeddingSchemaVersion,
		Provider:      SemanticEmbeddingProviderSiliconFlow,
		Model:         "BAAI/bge-m3",
		Dimensions:    512,
	})
	if err == nil || !strings.Contains(err.Error(), "dimensions = 512, want 1024") {
		t.Fatalf("WriteSemanticEmbeddingSettings() error = %v, want dimensions error", err)
	}
}

func TestWriteReadSiliconFlowSettingsUsesPresetDefaults(t *testing.T) {
	root := t.TempDir()
	err := WriteSemanticEmbeddingSettings(root, SemanticEmbeddingSettings{
		SchemaVersion: semanticEmbeddingSchemaVersion,
		Provider:      SemanticEmbeddingProviderSiliconFlow,
		Dimensions:    SemanticEmbeddingDimensions,
		ConnectionID:  "semantic_embedding.connection-1",
	})
	if err != nil {
		t.Fatalf("WriteSemanticEmbeddingSettings() error = %v", err)
	}
	settings, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatalf("ReadSemanticEmbeddingSettings() error = %v", err)
	}
	if !settings.Enabled || settings.Endpoint != semanticEmbeddingSiliconFlowURL || settings.Model != semanticEmbeddingSiliconFlowModel || settings.Dimensions != SemanticEmbeddingDimensions {
		t.Fatalf("settings = %#v", settings)
	}
	status := SemanticEmbeddingStatusFromSettings(settings)
	if !status.Enabled || !status.Configured || status.Provider != SemanticEmbeddingProviderSiliconFlow || status.Model != semanticEmbeddingSiliconFlowModel {
		t.Fatalf("status = %#v", status)
	}
}

func TestLegacyLocalBGENormalizesToNone(t *testing.T) {
	settings, err := normalizeSemanticEmbeddingSettings(SemanticEmbeddingSettings{
		SchemaVersion: 1,
		Provider:      SemanticEmbeddingProviderLocalBGE,
		Enabled:       true,
		Dimensions:    SemanticEmbeddingLegacyDimensions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Provider != SemanticEmbeddingProviderNone || settings.Enabled || settings.Dimensions != SemanticEmbeddingDimensions {
		t.Fatalf("settings = %#v", settings)
	}
}

func TestReadLegacy512SettingsRequiresExplicitReconfiguration(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "semantic_embedding")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"schema_version":1,"data":{"schema_version":1,"provider":"openai_compatible_api","enabled":true,"model":"legacy-embedding","dimensions":512}}`)
	if err := os.WriteFile(filepath.Join(dir, semanticEmbeddingSettingsFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatalf("ReadSemanticEmbeddingSettings() error = %v", err)
	}
	status := SemanticEmbeddingStatusFromSettings(settings)
	if settings.Enabled || settings.LegacyReason != "legacy_512_reconfiguration_required" || status.Configured || status.Reason != settings.LegacyReason {
		t.Fatalf("settings/status = %#v / %#v", settings, status)
	}
}

func TestConfigServiceSavesAndDeletesIndependentSemanticCredential(t *testing.T) {
	root := t.TempDir()
	secrets := NewTestSecretStore()
	service := NewConfigService(root, secrets)
	status, err := service.SemanticEmbeddingStatus()
	if err != nil {
		t.Fatalf("SemanticEmbeddingStatus() error = %v", err)
	}
	if status.Enabled || status.Configured || status.Provider != SemanticEmbeddingProviderNone {
		t.Fatalf("default status = %#v", status)
	}
	apiKey := "sf-test-secret"
	status, err = service.SaveSemanticEmbeddingSettings(SiliconFlowSemanticEmbeddingDefaults(), &apiKey)
	if err != nil {
		t.Fatalf("SaveSemanticEmbeddingSettings() error = %v", err)
	}
	if !status.Enabled || !status.Configured || !status.CredentialConfigured || status.Provider != SemanticEmbeddingProviderSiliconFlow {
		t.Fatalf("saved status = %#v", status)
	}
	raw, err := os.ReadFile(filepath.Join(root, "semantic_embedding", semanticEmbeddingSettingsFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), apiKey) {
		t.Fatalf("settings leaked API key: %s", raw)
	}
	settings, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	if secretNamespace(settings.ConnectionID) != "semantic_embedding" {
		t.Fatalf("secret namespace = %q", secretNamespace(settings.ConnectionID))
	}
	status, err = service.DeleteSemanticEmbeddingCredential()
	if err != nil {
		t.Fatalf("DeleteSemanticEmbeddingCredential() error = %v", err)
	}
	if status.Configured || status.CredentialConfigured || status.Reason != "semantic_embedding_credential_required" {
		t.Fatalf("deleted status = %#v", status)
	}
}

func TestSaveSemanticEmbeddingNoneDoesNotRequireSecretStore(t *testing.T) {
	service := NewConfigService(t.TempDir(), nil)
	status, err := service.SaveSemanticEmbeddingSettings(defaultSemanticEmbeddingSettings(), nil)
	if err != nil {
		t.Fatalf("SaveSemanticEmbeddingSettings(none) error = %v", err)
	}
	if status.Provider != SemanticEmbeddingProviderNone || status.Enabled || status.Configured {
		t.Fatalf("status = %#v", status)
	}
}
