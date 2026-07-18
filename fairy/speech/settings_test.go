package speech

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fairy/secret"
)

func TestReadStatusDefaultsToDisabledAndNoSecret(t *testing.T) {
	root := t.TempDir()
	status, err := ReadStatus(root, nil)
	if err != nil {
		t.Fatalf("ReadStatus() error = %v", err)
	}
	if status.Enabled || status.Configured || status.HasAPIKey || status.HasAccessToken {
		t.Fatalf("status = %#v, want disabled/unconfigured/no secret", status)
	}
	if status.BaseURL != DefaultBaseURL || status.TrainPath != DefaultTrainPath || status.QueryPath != DefaultQueryPath || status.UpgradePath != DefaultUpgradePath {
		t.Fatalf("defaults not applied: %#v", status)
	}
}

func TestSaveSettingsStoresAPIKeyAndReturnsRedactedStatus(t *testing.T) {
	root := t.TempDir()
	status, err := SaveSettings(root, SaveSettingsRequest{
		Enabled:        true,
		APIKey:         "test-api-key",
		DefaultSpeaker: "S_voice",
		DefaultFormat:  "wav",
	}, nil)
	if err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}
	if !status.Enabled || !status.Configured || !status.HasAPIKey || status.HasAccessToken {
		t.Fatalf("status = %#v, want enabled/configured/api key only", status)
	}
	wire, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("json.Marshal(status) error = %v", err)
	}
	if strings.Contains(string(wire), "test-api-key") || strings.Contains(string(wire), "X-Api-Key") {
		t.Fatalf("status leaked secret: %s", wire)
	}

	store, err := resolveSecretStore(root, nil)
	if err != nil {
		t.Fatalf("resolveSecretStore() error = %v", err)
	}
	value, ok, err := store.Load(apiKeySecretID)
	if err != nil {
		t.Fatalf("Load(api key) error = %v", err)
	}
	if !ok || value.Expose() != "test-api-key" {
		t.Fatalf("stored api key = %q ok=%v", value.Expose(), ok)
	}
}

func TestSaveSettingsRequiresCredentialWhenEnabled(t *testing.T) {
	root := t.TempDir()
	_, err := SaveSettings(root, SaveSettingsRequest{Enabled: true}, nil)
	if err == nil {
		t.Fatal("SaveSettings() error = nil, want missing credential")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("error = %q, want credential summary", err.Error())
	}
}

func TestSaveSettingsCanUseAPIKeyOnlyWhenEnabled(t *testing.T) {
	root := t.TempDir()
	status, err := SaveSettings(root, SaveSettingsRequest{Enabled: true, APIKey: "test-api-key"}, nil)
	if err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}
	if !status.Configured || !status.HasAPIKey || status.HasAccessToken {
		t.Fatalf("status = %#v, want configured with API key only", status)
	}
}

func TestSaveSettingsCanUseExistingAPIKey(t *testing.T) {
	root := t.TempDir()
	dbPath, err := secret.DatabasePath(root)
	if err != nil {
		t.Fatalf("DatabasePath() error = %v", err)
	}
	store := secret.NewStore(dbPath)
	value, err := secret.NewValue("existing-key")
	if err != nil {
		t.Fatalf("NewValue() error = %v", err)
	}
	if err := store.Save(apiKeySecretID, value); err != nil {
		t.Fatalf("Save(api key) error = %v", err)
	}
	status, err := SaveSettings(root, SaveSettingsRequest{Enabled: true}, store)
	if err != nil {
		t.Fatalf("SaveSettings() error = %v", err)
	}
	if !status.Configured || !status.HasAPIKey || status.HasAccessToken {
		t.Fatalf("status = %#v, want configured with existing api key", status)
	}
}

func TestParseSettingsMigratesLegacyVoiceCloneDefaultPaths(t *testing.T) {
	legacy := []byte("{\"schema_version\":1,\"data\":{\"schema_version\":1,\"enabled\":true,\"base_url\":\"https://openspeech.bytedance.com/api/v3/tts\",\"train_path\":\"/voice_clone\",\"query_path\":\"/query_voice\",\"upgrade_path\":\"/upgrade_voice\",\"app_id\":\"appid\",\"default_speaker\":\"S_voice\",\"default_language\":0,\"default_format\":\"wav\"}}")
	settings, err := ParseSettings(legacy)
	if err != nil {
		t.Fatalf("ParseSettings() error = %v", err)
	}
	if settings.BaseURL != DefaultBaseURL || settings.TrainPath != DefaultTrainPath || settings.QueryPath != DefaultQueryPath {
		t.Fatalf("legacy paths not migrated: %#v", settings)
	}
}

func TestLegacyWSSSettingsFileDoesNotConfigureVoiceCloneHTTP(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, "speech", "volcengine_tts.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	legacy := `{"schema_version":1,"data":{"schema_version":1,"enabled":true,"endpoint":"wss://openspeech.bytedance.com/api/v1/tts/ws_binary","app_id":"appid","resource_id":"seed-icl-2.0","cluster":"volcano_tts","speaker":"S_voice","format":"mp3","sample_rate":24000,"uid":"fairy"}}`
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	status, err := ReadStatus(root, nil)
	if err != nil {
		t.Fatalf("ReadStatus() error = %v", err)
	}
	if status.Enabled || status.Configured {
		t.Fatalf("legacy WSS status configured HTTP voice clone: %#v", status)
	}
	if strings.Contains(status.BaseURL, "ws_binary") {
		t.Fatalf("status retained WSS endpoint: %#v", status)
	}
}
