package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type semanticRuntimeRecorder struct {
	prepared   []SemanticEmbeddingSettings
	committed  int
	disabled   int
	prepareErr error
}

func (runtime *semanticRuntimeRecorder) PrepareSemanticEmbedding(settings SemanticEmbeddingSettings) (func(), error) {
	runtime.prepared = append(runtime.prepared, settings)
	if runtime.prepareErr != nil {
		return nil, runtime.prepareErr
	}
	return func() { runtime.committed++ }, nil
}

func (runtime *semanticRuntimeRecorder) DisableSemanticEmbedding() {
	runtime.disabled++
}

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
		Enabled:       true,
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

func TestSemanticEmbeddingSettingsPreserveDisabledProvider(t *testing.T) {
	root := t.TempDir()
	settings := SiliconFlowSemanticEmbeddingDefaults()
	settings.Enabled = false
	settings.ConnectionID = "semantic_embedding.disabled"
	if err := WriteSemanticEmbeddingSettings(root, settings); err != nil {
		t.Fatalf("WriteSemanticEmbeddingSettings() error = %v", err)
	}
	got, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatalf("ReadSemanticEmbeddingSettings() error = %v", err)
	}
	if got.Enabled || got.Provider != SemanticEmbeddingProviderSiliconFlow || got.Endpoint != semanticEmbeddingSiliconFlowURL || got.Model != semanticEmbeddingSiliconFlowModel || got.ConnectionID != settings.ConnectionID {
		t.Fatalf("disabled settings = %#v", got)
	}
	status := SemanticEmbeddingStatusFromSettings(got)
	if status.Enabled || status.Configured || status.Reason != "semantic_embedding_disabled" {
		t.Fatalf("disabled status = %#v", status)
	}
}

func TestSemanticEmbeddingSettingsRejectUnsafeEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"ftp://embedding.example.test/v1",
		"https://user:secret@embedding.example.test/v1",
		"https://embedding.example.test/v1?token=secret",
		"https://embedding.example.test/v1#fragment",
		"https://embedding.example.test/v1/embeddings",
		"https://embedding.example.test/v1/chat/completions",
	} {
		t.Run(endpoint, func(t *testing.T) {
			err := WriteSemanticEmbeddingSettings(t.TempDir(), SemanticEmbeddingSettings{
				SchemaVersion: semanticEmbeddingSchemaVersion,
				Provider:      SemanticEmbeddingProviderOpenAICompatible,
				Enabled:       true,
				Endpoint:      endpoint,
				Model:         "embedding-model",
				Dimensions:    SemanticEmbeddingDimensions,
			})
			if err == nil {
				t.Fatalf("WriteSemanticEmbeddingSettings(%q) error = nil", endpoint)
			}
		})
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

func TestConfigServiceDisabledSemanticProviderRetainsCredential(t *testing.T) {
	root := t.TempDir()
	secrets := NewTestSecretStore()
	service := NewConfigService(root, secrets)
	apiKey := "sf-disabled-secret"
	settings := SiliconFlowSemanticEmbeddingDefaults()
	if _, err := service.SaveSemanticEmbeddingSettings(settings, &apiKey); err != nil {
		t.Fatalf("SaveSemanticEmbeddingSettings(enabled) error = %v", err)
	}
	settings.Enabled = false
	status, err := service.SaveSemanticEmbeddingSettings(settings, nil)
	if err != nil {
		t.Fatalf("SaveSemanticEmbeddingSettings(disabled) error = %v", err)
	}
	if status.Enabled || status.Configured || !status.CredentialConfigured || status.Reason != "semantic_embedding_disabled" {
		t.Fatalf("disabled status = %#v", status)
	}
	persisted, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	secret, ok, err := secrets.Load(persisted.ConnectionID)
	if err != nil || !ok || secret.Expose() != apiKey {
		t.Fatalf("retained secret = (%v, %v, %v)", secret, ok, err)
	}
}

func TestConfigServicePublishesSemanticRuntimeAfterPersistence(t *testing.T) {
	root := t.TempDir()
	secrets := NewTestSecretStore()
	service := NewConfigService(root, secrets)
	runtime := &semanticRuntimeRecorder{}
	service.AttachSemanticEmbeddingRuntime(runtime)
	apiKey := "sf-runtime-secret"
	status, err := service.SaveSemanticEmbeddingSettings(SiliconFlowSemanticEmbeddingDefaults(), &apiKey)
	if err != nil {
		t.Fatalf("SaveSemanticEmbeddingSettings() error = %v", err)
	}
	if !status.Configured || len(runtime.prepared) != 1 || runtime.committed != 1 || !runtime.prepared[0].Enabled {
		t.Fatalf("runtime apply = status:%#v prepared:%#v committed:%d", status, runtime.prepared, runtime.committed)
	}
	if _, err := service.DeleteSemanticEmbeddingCredential(); err != nil {
		t.Fatalf("DeleteSemanticEmbeddingCredential() error = %v", err)
	}
	if runtime.disabled != 1 {
		t.Fatalf("runtime disabled = %d, want 1", runtime.disabled)
	}
}

func TestConfigServicePrepareFailureRestoresSemanticSecretAndSettings(t *testing.T) {
	root := t.TempDir()
	secrets := NewTestSecretStore()
	service := NewConfigService(root, secrets)
	oldKey := "old-runtime-secret"
	if _, err := service.SaveSemanticEmbeddingSettings(SiliconFlowSemanticEmbeddingDefaults(), &oldKey); err != nil {
		t.Fatal(err)
	}
	before, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &semanticRuntimeRecorder{prepareErr: errors.New("candidate rejected")}
	service.AttachSemanticEmbeddingRuntime(runtime)
	newKey := "new-runtime-secret"
	settings := SiliconFlowSemanticEmbeddingDefaults()
	settings.Endpoint = "https://other.embedding.example.test/v1"
	if _, err := service.SaveSemanticEmbeddingSettings(settings, &newKey); err == nil || !strings.Contains(err.Error(), "candidate rejected") {
		t.Fatalf("SaveSemanticEmbeddingSettings() error = %v", err)
	}
	after, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	secret, ok, err := secrets.Load(before.ConnectionID)
	if err != nil || !ok {
		t.Fatalf("Load(old secret) = %v, %v", ok, err)
	}
	if after != before || secret.Expose() != oldKey || runtime.committed != 0 {
		t.Fatalf("rollback = before:%#v after:%#v secret:%v committed:%d", before, after, secret, runtime.committed)
	}
}

func TestConfigServiceSettingsFailureDoesNotPublishAndRestoresSecret(t *testing.T) {
	root := t.TempDir()
	secrets := NewTestSecretStore()
	service := NewConfigService(root, secrets)
	oldKey := "old-settings-secret"
	if _, err := service.SaveSemanticEmbeddingSettings(SiliconFlowSemanticEmbeddingDefaults(), &oldKey); err != nil {
		t.Fatal(err)
	}
	before, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &semanticRuntimeRecorder{}
	service.AttachSemanticEmbeddingRuntime(runtime)
	service.writeSemanticSettings = func(string, SemanticEmbeddingSettings) error {
		return errors.New("settings storage unavailable")
	}
	newKey := "new-settings-secret"
	settings := SiliconFlowSemanticEmbeddingDefaults()
	settings.Endpoint = "https://new.embedding.example.test/v1"
	if _, err := service.SaveSemanticEmbeddingSettings(settings, &newKey); err == nil || !strings.Contains(err.Error(), "settings storage unavailable") {
		t.Fatalf("SaveSemanticEmbeddingSettings() error = %v", err)
	}
	after, err := ReadSemanticEmbeddingSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	secret, ok, err := secrets.Load(before.ConnectionID)
	if err != nil || !ok {
		t.Fatalf("Load(old secret) = %v, %v", ok, err)
	}
	if after != before || secret.Expose() != oldKey || len(runtime.prepared) != 1 || runtime.committed != 0 {
		t.Fatalf("settings rollback = before:%#v after:%#v secret:%v prepared:%d committed:%d", before, after, secret, len(runtime.prepared), runtime.committed)
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
