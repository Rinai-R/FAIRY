package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"fairy/search"
)

const webSearchSettingsFile = "settings.json"

type WebSearchSettings struct {
	SchemaVersion uint32 `json:"schema_version"`
	Enabled       bool   `json:"enabled"`
}

type WebSearchStatus struct {
	Enabled     bool   `json:"enabled"`
	BinaryPath  string `json:"binaryPath"`
	BinaryFound bool   `json:"binaryFound"`
}

type webSearchDocument struct {
	SchemaVersion uint32            `json:"schema_version"`
	Data          WebSearchSettings `json:"data"`
}

func webSearchDir(root string) string {
	return filepath.Join(root, "web_search")
}

func ReadWebSearchSettings(root string) (WebSearchSettings, error) {
	path := filepath.Join(webSearchDir(root), webSearchSettingsFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WebSearchSettings{SchemaVersion: 1, Enabled: false}, nil
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
	return doc.Data, nil
}

func WriteWebSearchSettings(root string, settings WebSearchSettings) error {
	if settings.SchemaVersion == 0 {
		settings.SchemaVersion = 1
	}
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
	binaryPath, found := search.ResolveBinary(s.root)
	return WebSearchStatus{
		Enabled:     settings.Enabled,
		BinaryPath:  binaryPath,
		BinaryFound: found,
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
