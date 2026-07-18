package speech

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fairy/secret"
)

const (
	settingsPath = "speech/volcengine_voice_clone_http.json"

	apiKeySecretID      = "speech.volcengine_voice_clone.api_key"
	accessTokenSecretID = "speech.volcengine_voice_clone.access_token"

	DefaultBaseURL             = "https://openspeech.bytedance.com"
	DefaultTrainPath           = "/api/v3/tts/voice_clone"
	DefaultQueryPath           = "/api/v3/tts/get_voice"
	DefaultUpgradePath         = "/upgrade_voice"
	DefaultSynthesizePath      = "/api/v3/tts/unidirectional"
	DefaultResourceID          = "seed-icl-2.0"
	DefaultSynthesisResourceID = "seed-icl-1.0"
	DefaultSynthesisModel      = ""
	DefaultTrainSource         = 2
	DefaultTrainModelType      = 1
	DefaultAudioFormat         = "wav"
	DefaultSynthesisFormat     = "mp3"
	DefaultSynthesisSampleRate = 24000
	DefaultLanguage            = 0
	DefaultExtraDenoiseID      = ""
	DefaultMaxProviderBytes    = 2 * 1024 * 1024

	legacyDefaultBaseURL   = "https://openspeech.bytedance.com/api/v3/tts"
	legacyDefaultTrainPath = "/voice_clone"
	legacyDefaultQueryPath = "/query_voice"
)

var (
	ErrConfigRootRequired  = errors.New("config root is required")
	ErrVoiceCloneDisabled  = errors.New("volcengine voice clone http is disabled")
	ErrSpeakerIDRequired   = errors.New("speaker_id is required")
	ErrAudioDataRequired   = errors.New("audio data is required")
	ErrAudioFormatRequired = errors.New("audio format is required")
)

type Settings struct {
	Enabled             bool
	BaseURL             string
	TrainPath           string
	QueryPath           string
	UpgradePath         string
	AppID               string
	SynthesisResourceID string
	SynthesisModel      string
	DefaultSpeaker      string
	DefaultLanguage     int
	DefaultFormat       string
}

type Credentials struct {
	APIKey         secret.Value
	HasAPIKey      bool
	AccessToken    secret.Value
	HasAccessToken bool
}

type Status struct {
	Configured          bool   `json:"configured"`
	Enabled             bool   `json:"enabled"`
	BaseURL             string `json:"baseUrl"`
	TrainPath           string `json:"trainPath"`
	QueryPath           string `json:"queryPath"`
	UpgradePath         string `json:"upgradePath"`
	AppID               string `json:"appId"`
	SynthesisResourceID string `json:"synthesisResourceId"`
	SynthesisModel      string `json:"synthesisModel"`
	DefaultSpeaker      string `json:"defaultSpeaker"`
	DefaultLanguage     int    `json:"defaultLanguage"`
	DefaultFormat       string `json:"defaultFormat"`
	HasAPIKey           bool   `json:"hasApiKey"`
	HasAccessToken      bool   `json:"hasAccessToken"`
	SecretMigrated      bool   `json:"secretMigrated"`
}

type SaveSettingsRequest struct {
	Enabled             bool   `json:"enabled"`
	BaseURL             string `json:"baseUrl"`
	TrainPath           string `json:"trainPath"`
	QueryPath           string `json:"queryPath"`
	UpgradePath         string `json:"upgradePath"`
	AppID               string `json:"appId"`
	SynthesisResourceID string `json:"synthesisResourceId"`
	SynthesisModel      string `json:"synthesisModel"`
	APIKey              string `json:"apiKey"`
	AccessToken         string `json:"accessToken"`
	ClearAPIKey         bool   `json:"clearApiKey"`
	ClearAccessToken    bool   `json:"clearAccessToken"`
	DefaultSpeaker      string `json:"defaultSpeaker"`
	DefaultLanguage     int    `json:"defaultLanguage"`
	DefaultFormat       string `json:"defaultFormat"`
}

type settingsDocument struct {
	SchemaVersion uint32         `json:"schema_version"`
	Data          storedSettings `json:"data"`
}

type storedSettings struct {
	SchemaVersion       uint32 `json:"schema_version"`
	Enabled             bool   `json:"enabled"`
	BaseURL             string `json:"base_url"`
	TrainPath           string `json:"train_path"`
	QueryPath           string `json:"query_path"`
	UpgradePath         string `json:"upgrade_path"`
	AppID               string `json:"app_id"`
	SynthesisResourceID string `json:"synthesis_resource_id"`
	SynthesisModel      string `json:"synthesis_model"`
	DefaultSpeaker      string `json:"default_speaker"`
	DefaultLanguage     int    `json:"default_language"`
	DefaultFormat       string `json:"default_format"`
}

func DefaultSettings() Settings {
	return Settings{
		BaseURL:             DefaultBaseURL,
		TrainPath:           DefaultTrainPath,
		QueryPath:           DefaultQueryPath,
		UpgradePath:         DefaultUpgradePath,
		SynthesisResourceID: DefaultSynthesisResourceID,
		SynthesisModel:      DefaultSynthesisModel,
		DefaultLanguage:     DefaultLanguage,
		DefaultFormat:       DefaultAudioFormat,
	}
}

func ReadStatus(root string, secrets *secret.Store) (Status, error) {
	settings, err := ReadSettings(root)
	if err != nil {
		return Status{}, err
	}
	store, err := resolveSecretStore(root, secrets)
	if err != nil {
		return Status{}, err
	}
	_, hasAPIKey, err := store.Load(apiKeySecretID)
	if err != nil {
		return Status{}, err
	}
	_, hasAccessToken, err := store.Load(accessTokenSecretID)
	if err != nil {
		return Status{}, err
	}
	return statusFromSettings(settings, hasAPIKey, hasAccessToken), nil
}

func ReadSettings(root string) (Settings, error) {
	if root == "" {
		return Settings{}, ErrConfigRootRequired
	}
	data, err := os.ReadFile(filepath.Join(root, settingsPath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultSettings(), nil
		}
		return Settings{}, fmt.Errorf("reading volcengine voice clone settings: %w", err)
	}
	return ParseSettings(data)
}

func ParseSettings(data []byte) (Settings, error) {
	var doc settingsDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return Settings{}, fmt.Errorf("parsing volcengine voice clone settings: %w", err)
	}
	if doc.SchemaVersion != 1 || doc.Data.SchemaVersion != 1 {
		return Settings{}, errors.New("unsupported volcengine voice clone settings schema")
	}
	return withDefaults(Settings{
		Enabled:             doc.Data.Enabled,
		BaseURL:             doc.Data.BaseURL,
		TrainPath:           doc.Data.TrainPath,
		QueryPath:           doc.Data.QueryPath,
		UpgradePath:         doc.Data.UpgradePath,
		AppID:               doc.Data.AppID,
		SynthesisResourceID: doc.Data.SynthesisResourceID,
		SynthesisModel:      doc.Data.SynthesisModel,
		DefaultSpeaker:      doc.Data.DefaultSpeaker,
		DefaultLanguage:     doc.Data.DefaultLanguage,
		DefaultFormat:       doc.Data.DefaultFormat,
	}), nil
}

func SaveSettings(root string, input SaveSettingsRequest, secrets *secret.Store) (Status, error) {
	if root == "" {
		return Status{}, ErrConfigRootRequired
	}
	settings := compileSettings(input)
	store, err := resolveSecretStore(root, secrets)
	if err != nil {
		return Status{}, err
	}
	if input.ClearAPIKey {
		if err := store.Delete(apiKeySecretID); err != nil {
			return Status{}, err
		}
	}
	if input.ClearAccessToken {
		if err := store.Delete(accessTokenSecretID); err != nil {
			return Status{}, err
		}
	}
	if input.APIKey != "" {
		value, err := secret.NewValue(input.APIKey)
		if err != nil {
			return Status{}, err
		}
		if err := store.Save(apiKeySecretID, value); err != nil {
			return Status{}, err
		}
	}
	if input.AccessToken != "" {
		value, err := secret.NewValue(input.AccessToken)
		if err != nil {
			return Status{}, err
		}
		if err := store.Save(accessTokenSecretID, value); err != nil {
			return Status{}, err
		}
	}
	_, hasAPIKey, err := store.Load(apiKeySecretID)
	if err != nil {
		return Status{}, err
	}
	_, hasAccessToken, err := store.Load(accessTokenSecretID)
	if err != nil {
		return Status{}, err
	}
	if settings.Enabled {
		if err := validateReady(settings, hasAPIKey, hasAccessToken); err != nil {
			return Status{}, err
		}
	}
	if err := writeSettings(root, settings); err != nil {
		return Status{}, err
	}
	return statusFromSettings(settings, hasAPIKey, hasAccessToken), nil
}

func ClearSettings(root string, secrets *secret.Store) (Status, error) {
	if root == "" {
		return Status{}, ErrConfigRootRequired
	}
	store, err := resolveSecretStore(root, secrets)
	if err != nil {
		return Status{}, err
	}
	if err := store.Delete(apiKeySecretID); err != nil {
		return Status{}, err
	}
	if err := store.Delete(accessTokenSecretID); err != nil {
		return Status{}, err
	}
	if err := os.Remove(filepath.Join(root, settingsPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Status{}, fmt.Errorf("clearing volcengine voice clone settings: %w", err)
	}
	return statusFromSettings(DefaultSettings(), false, false), nil
}

func loadReadySettings(root string, secrets *secret.Store) (Settings, Credentials, error) {
	settings, err := ReadSettings(root)
	if err != nil {
		return Settings{}, Credentials{}, err
	}
	if !settings.Enabled {
		return Settings{}, Credentials{}, ErrVoiceCloneDisabled
	}
	store, err := resolveSecretStore(root, secrets)
	if err != nil {
		return Settings{}, Credentials{}, err
	}
	apiKey, hasAPIKey, err := store.Load(apiKeySecretID)
	if err != nil {
		return Settings{}, Credentials{}, err
	}
	accessToken, hasAccessToken, err := store.Load(accessTokenSecretID)
	if err != nil {
		return Settings{}, Credentials{}, err
	}
	if err := validateReady(settings, hasAPIKey, hasAccessToken); err != nil {
		return Settings{}, Credentials{}, err
	}
	return settings, Credentials{APIKey: apiKey, HasAPIKey: hasAPIKey, AccessToken: accessToken, HasAccessToken: hasAccessToken}, nil
}

func compileSettings(input SaveSettingsRequest) Settings {
	return withDefaults(Settings{
		Enabled:             input.Enabled,
		BaseURL:             input.BaseURL,
		TrainPath:           input.TrainPath,
		QueryPath:           input.QueryPath,
		UpgradePath:         input.UpgradePath,
		AppID:               strings.TrimSpace(input.AppID),
		SynthesisResourceID: strings.TrimSpace(input.SynthesisResourceID),
		SynthesisModel:      strings.TrimSpace(input.SynthesisModel),
		DefaultSpeaker:      input.DefaultSpeaker,
		DefaultLanguage:     input.DefaultLanguage,
		DefaultFormat:       input.DefaultFormat,
	})
}

func withDefaults(settings Settings) Settings {
	settings.BaseURL = trimTrailingSlash(defaultString(settings.BaseURL, DefaultBaseURL))
	settings.TrainPath = normalizePath(defaultString(settings.TrainPath, DefaultTrainPath))
	settings.QueryPath = normalizePath(defaultString(settings.QueryPath, DefaultQueryPath))
	settings.UpgradePath = normalizePath(defaultString(settings.UpgradePath, DefaultUpgradePath))
	settings = migrateLegacyDefaultPaths(settings)
	settings.AppID = strings.TrimSpace(settings.AppID)
	settings.SynthesisResourceID = strings.TrimSpace(defaultString(settings.SynthesisResourceID, DefaultSynthesisResourceID))
	settings.SynthesisModel = strings.TrimSpace(defaultString(settings.SynthesisModel, DefaultSynthesisModel))
	settings.DefaultSpeaker = strings.TrimSpace(settings.DefaultSpeaker)
	settings.DefaultFormat = normalizeFormat(defaultString(settings.DefaultFormat, DefaultAudioFormat))
	if settings.DefaultLanguage < 0 {
		settings.DefaultLanguage = DefaultLanguage
	}
	return settings
}

func migrateLegacyDefaultPaths(settings Settings) Settings {
	if !strings.EqualFold(settings.BaseURL, legacyDefaultBaseURL) {
		return settings
	}
	migrated := false
	if settings.TrainPath == legacyDefaultTrainPath {
		settings.TrainPath = DefaultTrainPath
		migrated = true
	}
	if settings.QueryPath == legacyDefaultQueryPath {
		settings.QueryPath = DefaultQueryPath
		migrated = true
	}
	if migrated {
		settings.BaseURL = DefaultBaseURL
	}
	return settings
}

func validateReady(settings Settings, hasAPIKey bool, hasAccessToken bool) error {
	settings = withDefaults(settings)
	var missing []string
	if settings.BaseURL == "" {
		missing = append(missing, "base_url")
	}
	if settings.TrainPath == "" {
		missing = append(missing, "train_path")
	}
	if settings.QueryPath == "" {
		missing = append(missing, "query_path")
	}
	if settings.UpgradePath == "" {
		missing = append(missing, "upgrade_path")
	}
	if !hasAPIKey {
		missing = append(missing, "api_key")
	}
	if len(missing) > 0 {
		return fmt.Errorf("volcengine voice clone settings missing required fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

func statusFromSettings(settings Settings, hasAPIKey bool, hasAccessToken bool) Status {
	settings = withDefaults(settings)
	configured := settings.Enabled && validateReady(settings, hasAPIKey, hasAccessToken) == nil
	return Status{
		Configured:          configured,
		Enabled:             settings.Enabled,
		BaseURL:             settings.BaseURL,
		TrainPath:           settings.TrainPath,
		QueryPath:           settings.QueryPath,
		UpgradePath:         settings.UpgradePath,
		AppID:               settings.AppID,
		SynthesisResourceID: settings.SynthesisResourceID,
		SynthesisModel:      settings.SynthesisModel,
		DefaultSpeaker:      settings.DefaultSpeaker,
		DefaultLanguage:     settings.DefaultLanguage,
		DefaultFormat:       settings.DefaultFormat,
		HasAPIKey:           hasAPIKey,
		HasAccessToken:      hasAccessToken,
		SecretMigrated:      true,
	}
}

func writeSettings(root string, settings Settings) error {
	settings = withDefaults(settings)
	doc := settingsDocument{
		SchemaVersion: 1,
		Data: storedSettings{
			SchemaVersion:       1,
			Enabled:             settings.Enabled,
			BaseURL:             settings.BaseURL,
			TrainPath:           settings.TrainPath,
			QueryPath:           settings.QueryPath,
			UpgradePath:         settings.UpgradePath,
			AppID:               settings.AppID,
			SynthesisResourceID: settings.SynthesisResourceID,
			SynthesisModel:      settings.SynthesisModel,
			DefaultSpeaker:      settings.DefaultSpeaker,
			DefaultLanguage:     settings.DefaultLanguage,
			DefaultFormat:       settings.DefaultFormat,
		},
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding volcengine voice clone settings: %w", err)
	}
	filename := filepath.Join(root, settingsPath)
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		return fmt.Errorf("creating volcengine voice clone settings directory: %w", err)
	}
	if err := os.WriteFile(filename, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing volcengine voice clone settings: %w", err)
	}
	return nil
}

func resolveSecretStore(root string, secrets *secret.Store) (*secret.Store, error) {
	if secrets != nil {
		return secrets, nil
	}
	dbPath, err := secret.DatabasePath(root)
	if err != nil {
		return nil, err
	}
	return secret.NewStore(dbPath), nil
}

func defaultString(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func normalizeFormat(format string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(format)), ".")
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func trimTrailingSlash(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}
