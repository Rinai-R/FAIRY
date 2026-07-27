package config

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	webSearchSettingsFile   = "settings.json"
	defaultWebSearchBaseURL = "http://127.0.0.1:7000"
	webSearchBaseURLEnv     = "FAIRY_OPENSERP_URL"
	webSearchHealthTimeout  = 5 * time.Second
)

type WebSearchSettings struct {
	SchemaVersion uint32 `json:"schema_version"`
	Enabled       bool   `json:"enabled"`
	// BaseURL is the OpenSERP HTTP origin (docker compose). Empty uses FAIRY_OPENSERP_URL or http://127.0.0.1:7000.
	BaseURL string `json:"base_url,omitempty"`
}

type WebSearchStatus struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"baseUrl"`
	Ready   bool   `json:"ready"`
}

type webSearchDocument struct {
	SchemaVersion uint32            `json:"schema_version"`
	Data          WebSearchSettings `json:"data"`
}

func webSearchDir(root string) string {
	return filepath.Join(root, "web_search")
}

func ResolveWebSearchBaseURL(raw string) string {
	baseURL := strings.TrimSpace(raw)
	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv(webSearchBaseURLEnv))
	}
	if baseURL == "" {
		baseURL = defaultWebSearchBaseURL
	}
	return strings.TrimRight(baseURL, "/")
}

func webSearchReady(ctx context.Context, baseURL string) bool {
	healthCtx, cancel := context.WithTimeout(ctx, webSearchHealthTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(healthCtx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/health", nil)
	if err != nil {
		return false
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices
}

func ReadWebSearchSettings(root string) (WebSearchSettings, error) {
	path := filepath.Join(webSearchDir(root), webSearchSettingsFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WebSearchSettings{SchemaVersion: 1, Enabled: true}, nil
		}
		return WebSearchSettings{}, err
	}
	var doc webSearchDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return WebSearchSettings{}, err
	}
	if doc.Data.SchemaVersion == 0 {
		doc.Data.SchemaVersion = doc.SchemaVersion
	}
	if doc.Data.SchemaVersion == 0 {
		doc.Data.SchemaVersion = 1
	}
	doc.Data.BaseURL = strings.TrimSpace(doc.Data.BaseURL)
	return doc.Data, nil
}

func WriteWebSearchSettings(root string, settings WebSearchSettings) error {
	if settings.SchemaVersion == 0 {
		settings.SchemaVersion = 1
	}
	settings.BaseURL = strings.TrimSpace(settings.BaseURL)
	if err := os.MkdirAll(webSearchDir(root), 0o755); err != nil {
		return err
	}
	doc := webSearchDocument{SchemaVersion: 1, Data: settings}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(webSearchDir(root), webSearchSettingsFile), raw, 0o600)
}

func (s *ConfigService) WebSearchStatus() (WebSearchStatus, error) {
	settings, err := ReadWebSearchSettings(s.root)
	if err != nil {
		return WebSearchStatus{}, err
	}
	baseURL := ResolveWebSearchBaseURL(settings.BaseURL)
	ready := false
	if settings.Enabled {
		ready = webSearchReady(context.Background(), baseURL)
	}
	return WebSearchStatus{
		Enabled: settings.Enabled,
		BaseURL: baseURL,
		Ready:   ready,
	}, nil
}

func (s *ConfigService) SetWebSearchEnabled(enabled bool) (WebSearchStatus, error) {
	settings, err := ReadWebSearchSettings(s.root)
	if err != nil {
		return WebSearchStatus{}, err
	}
	settings.Enabled = enabled
	if settings.SchemaVersion == 0 {
		settings.SchemaVersion = 1
	}
	if err := WriteWebSearchSettings(s.root, settings); err != nil {
		return WebSearchStatus{}, err
	}
	return s.WebSearchStatus()
}

// SaveWebSearchSettings persists enabled + optional OpenSERP base URL.
func (s *ConfigService) SaveWebSearchSettings(settings WebSearchSettings) (WebSearchStatus, error) {
	if settings.SchemaVersion == 0 {
		settings.SchemaVersion = 1
	}
	settings.BaseURL = strings.TrimSpace(settings.BaseURL)
	if err := WriteWebSearchSettings(s.root, settings); err != nil {
		return WebSearchStatus{}, err
	}
	return s.WebSearchStatus()
}
