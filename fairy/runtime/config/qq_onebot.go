//go:build !endpointstrict

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	qqOneBotSchemaVersion = 1
	qqOneBotSettingsFile  = "settings.json"
	MaxQQGroupAllowlist   = 256
)

var ErrInvalidQQOneBotSettings = errors.New("invalid QQ OneBot settings")

type QQOneBotSettings struct {
	SchemaVersion  uint32   `json:"schemaVersion"`
	GroupAllowlist []string `json:"groupAllowlist"`
}

type qqOneBotDocument struct {
	SchemaVersion uint32           `json:"schema_version"`
	Data          QQOneBotSettings `json:"data"`
}

func qqOneBotDir(root string) string {
	return filepath.Join(root, "qq_onebot")
}

func defaultQQOneBotSettings() QQOneBotSettings {
	return QQOneBotSettings{SchemaVersion: qqOneBotSchemaVersion, GroupAllowlist: []string{}}
}

func ReadQQOneBotSettings(root string) (QQOneBotSettings, error) {
	if root == "" {
		return QQOneBotSettings{}, errors.New("config root is required")
	}
	raw, err := os.ReadFile(filepath.Join(qqOneBotDir(root), qqOneBotSettingsFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultQQOneBotSettings(), nil
		}
		return QQOneBotSettings{}, fmt.Errorf("reading QQ OneBot settings: %w", err)
	}
	var doc qqOneBotDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		return QQOneBotSettings{}, fmt.Errorf("parsing QQ OneBot settings: %w", err)
	}
	if doc.SchemaVersion != qqOneBotSchemaVersion {
		return QQOneBotSettings{}, fmt.Errorf("QQ OneBot document schema_version = %d, want %d", doc.SchemaVersion, qqOneBotSchemaVersion)
	}
	return normalizeQQOneBotSettings(doc.Data)
}

func WriteQQOneBotSettings(root string, settings QQOneBotSettings) (QQOneBotSettings, error) {
	if root == "" {
		return QQOneBotSettings{}, errors.New("config root is required")
	}
	normalized, err := normalizeQQOneBotSettings(settings)
	if err != nil {
		return QQOneBotSettings{}, err
	}
	dir := qqOneBotDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return QQOneBotSettings{}, fmt.Errorf("creating QQ OneBot settings directory: %w", err)
	}
	doc := qqOneBotDocument{SchemaVersion: qqOneBotSchemaVersion, Data: normalized}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return QQOneBotSettings{}, fmt.Errorf("serializing QQ OneBot settings: %w", err)
	}
	raw = append(raw, '\n')

	temporary, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return QQOneBotSettings{}, fmt.Errorf("creating temporary QQ OneBot settings: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return QQOneBotSettings{}, fmt.Errorf("securing temporary QQ OneBot settings: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return QQOneBotSettings{}, fmt.Errorf("writing temporary QQ OneBot settings: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return QQOneBotSettings{}, fmt.Errorf("syncing temporary QQ OneBot settings: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return QQOneBotSettings{}, fmt.Errorf("closing temporary QQ OneBot settings: %w", err)
	}
	if err := os.Rename(temporaryName, filepath.Join(dir, qqOneBotSettingsFile)); err != nil {
		return QQOneBotSettings{}, fmt.Errorf("replacing QQ OneBot settings: %w", err)
	}
	return normalized, nil
}

func normalizeQQOneBotSettings(settings QQOneBotSettings) (QQOneBotSettings, error) {
	if settings.SchemaVersion == 0 {
		settings.SchemaVersion = qqOneBotSchemaVersion
	}
	if settings.SchemaVersion != qqOneBotSchemaVersion {
		return QQOneBotSettings{}, fmt.Errorf("%w: schemaVersion = %d, want %d", ErrInvalidQQOneBotSettings, settings.SchemaVersion, qqOneBotSchemaVersion)
	}
	normalized := make([]string, 0, len(settings.GroupAllowlist))
	seen := make(map[string]struct{}, len(settings.GroupAllowlist))
	for _, raw := range settings.GroupAllowlist {
		value := strings.TrimSpace(raw)
		if value == "" {
			return QQOneBotSettings{}, fmt.Errorf("%w: QQ group number must be a positive decimal integer", ErrInvalidQQOneBotSettings)
		}
		for _, character := range value {
			if character < '0' || character > '9' {
				return QQOneBotSettings{}, fmt.Errorf("%w: QQ group number %q must be a positive decimal integer", ErrInvalidQQOneBotSettings, value)
			}
		}
		id, err := strconv.ParseUint(value, 10, 64)
		if err != nil || id == 0 {
			return QQOneBotSettings{}, fmt.Errorf("%w: QQ group number %q must be a positive decimal integer within uint64 range", ErrInvalidQQOneBotSettings, value)
		}
		canonical := strconv.FormatUint(id, 10)
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
		if len(normalized) > MaxQQGroupAllowlist {
			return QQOneBotSettings{}, fmt.Errorf("%w: QQ group allowlist exceeds %d unique entries", ErrInvalidQQOneBotSettings, MaxQQGroupAllowlist)
		}
	}
	return QQOneBotSettings{SchemaVersion: qqOneBotSchemaVersion, GroupAllowlist: normalized}, nil
}
