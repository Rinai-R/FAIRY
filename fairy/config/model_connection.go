package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fairy/secret"
)

const modelConnectionPath = "model/connection.json"

type ModelConnectionStatus struct {
	Configured            bool                `json:"configured"`
	Protocol              string              `json:"protocol,omitempty"`
	Endpoint              string              `json:"endpoint,omitempty"`
	Model                 string              `json:"model,omitempty"`
	ContextWindowTokens   uint64              `json:"contextWindowTokens,omitempty"`
	AuthMode              string              `json:"authMode,omitempty"`
	Capabilities          GatewayCapabilities `json:"capabilities,omitempty"`
	SecretStorageMigrated bool                `json:"secretStorageMigrated"`
}

type ModelConnection struct {
	ConnectionID        string
	Protocol            string
	Endpoint            string
	Model               string
	ContextWindowTokens uint64
	AuthMode            string
	Capabilities        GatewayCapabilities
}

type ModelConnectionInput struct {
	Protocol            string `json:"protocol"`
	Endpoint            string `json:"endpoint"`
	Model               string `json:"model"`
	ContextWindowTokens uint64 `json:"contextWindowTokens"`
	AuthMode            string `json:"authMode"`
}

type GatewayCapabilities struct {
	PromptCacheKey        bool `json:"promptCacheKey"`
	CachedTokensUsage     bool `json:"cachedTokensUsage"`
	ExplicitBreakpoints   bool `json:"explicitBreakpoints"`
	CacheRetention        bool `json:"cacheRetention"`
	WebsocketContinuation bool `json:"websocketContinuation"`
}

type modelConnectionDocument struct {
	SchemaVersion uint32                `json:"schema_version"`
	Data          modelConnectionConfig `json:"data"`
}

type modelConnectionConfig struct {
	SchemaVersion       uint32                    `json:"schema_version"`
	ConnectionID        string                    `json:"connection_id"`
	Protocol            string                    `json:"protocol"`
	Endpoint            string                    `json:"endpoint"`
	Model               string                    `json:"model"`
	ContextWindowTokens uint64                    `json:"context_window_tokens"`
	AuthMode            string                    `json:"auth_mode"`
	Capabilities        storedGatewayCapabilities `json:"capabilities"`
}

type storedGatewayCapabilities struct {
	PromptCacheKey        bool `json:"prompt_cache_key"`
	CachedTokensUsage     bool `json:"cached_tokens_usage"`
	ExplicitBreakpoints   bool `json:"explicit_breakpoints"`
	CacheRetention        bool `json:"cache_retention"`
	WebsocketContinuation bool `json:"websocket_continuation"`
}

func (c storedGatewayCapabilities) public() GatewayCapabilities {
	return GatewayCapabilities{
		PromptCacheKey:        c.PromptCacheKey,
		CachedTokensUsage:     c.CachedTokensUsage,
		ExplicitBreakpoints:   c.ExplicitBreakpoints,
		CacheRetention:        c.CacheRetention,
		WebsocketContinuation: c.WebsocketContinuation,
	}
}

func ReadModelConnectionStatus(root string) (ModelConnectionStatus, error) {
	if root == "" {
		return ModelConnectionStatus{}, errors.New("config root is required")
	}
	filename := filepath.Join(root, modelConnectionPath)
	data, err := os.ReadFile(filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ModelConnectionStatus{Configured: false, SecretStorageMigrated: true}, nil
		}
		return ModelConnectionStatus{}, fmt.Errorf("reading model connection status: %w", err)
	}
	return ParseModelConnectionStatus(data)
}

func ReadModelConnection(root string) (ModelConnection, error) {
	if root == "" {
		return ModelConnection{}, errors.New("config root is required")
	}
	filename := filepath.Join(root, modelConnectionPath)
	data, err := os.ReadFile(filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ModelConnection{}, errors.New("model connection is not configured")
		}
		return ModelConnection{}, fmt.Errorf("reading model connection: %w", err)
	}
	return ParseModelConnection(data)
}

func SaveModelConnection(root string, input ModelConnectionInput, apiKey *string, secrets *secret.Store) (ModelConnectionStatus, error) {
	if root == "" {
		return ModelConnectionStatus{}, errors.New("config root is required")
	}
	existing, err := ReadModelConnection(root)
	if err != nil && err.Error() != "model connection is not configured" {
		return ModelConnectionStatus{}, err
	}
	connectionID := existing.ConnectionID
	if connectionID == "" {
		connectionID = newID()
	}
	connection, err := compileModelConnection(connectionID, input)
	if err != nil {
		return ModelConnectionStatus{}, err
	}
	store, err := resolveSecretStore(root, secrets)
	if err != nil {
		return ModelConnectionStatus{}, err
	}
	if connection.AuthMode == "bearer_key" {
		if apiKey != nil {
			value, err := secret.NewValue(*apiKey)
			if err != nil {
				return ModelConnectionStatus{}, err
			}
			if err := store.Save(connectionID, value); err != nil {
				return ModelConnectionStatus{}, err
			}
		} else {
			_, ok, err := store.Load(connectionID)
			if err != nil {
				return ModelConnectionStatus{}, err
			}
			if !ok {
				return ModelConnectionStatus{}, errors.New("bearer_key connection requires model credential")
			}
		}
	} else {
		if apiKey != nil {
			return ModelConnectionStatus{}, errors.New("no_auth connection must not include model credential")
		}
		if existing.AuthMode == "bearer_key" {
			if err := store.Delete(connectionID); err != nil {
				return ModelConnectionStatus{}, err
			}
		}
	}
	if err := writeModelConnection(root, connection); err != nil {
		return ModelConnectionStatus{}, err
	}
	return statusFromConnection(connection), nil
}

func ClearModelConnection(root string, secrets *secret.Store) (bool, error) {
	existing, err := ReadModelConnection(root)
	if err != nil {
		if err.Error() == "model connection is not configured" {
			return false, nil
		}
		return false, err
	}
	if existing.AuthMode == "bearer_key" {
		store, err := resolveSecretStore(root, secrets)
		if err != nil {
			return false, err
		}
		if err := store.Delete(existing.ConnectionID); err != nil {
			return false, err
		}
	}
	if err := os.Remove(filepath.Join(root, modelConnectionPath)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("clearing model connection: %w", err)
	}
	return true, nil
}

// resolveSecretStore prefers an injected store from main; when nil it opens the
// path-handle under root (test-friendly fallback).
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

func ParseModelConnectionStatus(data []byte) (ModelConnectionStatus, error) {
	connection, err := ParseModelConnection(data)
	if err != nil {
		return ModelConnectionStatus{}, err
	}
	return statusFromConnection(connection), nil
}

func statusFromConnection(connection ModelConnection) ModelConnectionStatus {
	return ModelConnectionStatus{
		Configured:            true,
		Protocol:              connection.Protocol,
		Endpoint:              connection.Endpoint,
		Model:                 connection.Model,
		ContextWindowTokens:   connection.ContextWindowTokens,
		AuthMode:              connection.AuthMode,
		Capabilities:          connection.Capabilities,
		SecretStorageMigrated: true,
	}
}

func ParseModelConnection(data []byte) (ModelConnection, error) {
	var document modelConnectionDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return ModelConnection{}, fmt.Errorf("parsing model connection document: %w", err)
	}
	if document.SchemaVersion != 1 {
		return ModelConnection{}, fmt.Errorf("model connection document schema_version = %d, want 1", document.SchemaVersion)
	}
	config := document.Data
	if config.SchemaVersion != 3 {
		return ModelConnection{}, fmt.Errorf("model connection schema_version = %d, want 3", config.SchemaVersion)
	}
	if config.ConnectionID == "" {
		return ModelConnection{}, errors.New("model connection_id is required")
	}
	if config.Protocol != "responses" && config.Protocol != "chat_completions" {
		return ModelConnection{}, fmt.Errorf("model protocol %q is not supported", config.Protocol)
	}
	if config.Endpoint == "" {
		return ModelConnection{}, errors.New("model endpoint is required")
	}
	if config.Model == "" {
		return ModelConnection{}, errors.New("model name is required")
	}
	if config.ContextWindowTokens == 0 {
		return ModelConnection{}, errors.New("model context_window_tokens is required")
	}
	if config.AuthMode != "bearer_key" && config.AuthMode != "no_auth" {
		return ModelConnection{}, fmt.Errorf("model auth_mode %q is not supported", config.AuthMode)
	}
	return ModelConnection{
		ConnectionID:        config.ConnectionID,
		Protocol:            config.Protocol,
		Endpoint:            config.Endpoint,
		Model:               config.Model,
		ContextWindowTokens: config.ContextWindowTokens,
		AuthMode:            config.AuthMode,
		Capabilities:        config.Capabilities.public(),
	}, nil
}

func compileModelConnection(connectionID string, input ModelConnectionInput) (ModelConnection, error) {
	if connectionID == "" || strings.TrimSpace(connectionID) != connectionID {
		return ModelConnection{}, errors.New("model connection_id is required")
	}
	if input.Protocol != "responses" && input.Protocol != "chat_completions" {
		return ModelConnection{}, fmt.Errorf("model protocol %q is not supported", input.Protocol)
	}
	if input.Endpoint == "" || strings.TrimSpace(input.Endpoint) != input.Endpoint {
		return ModelConnection{}, errors.New("model endpoint is required")
	}
	if input.Model == "" || strings.TrimSpace(input.Model) != input.Model {
		return ModelConnection{}, errors.New("model name is required")
	}
	if input.ContextWindowTokens == 0 {
		return ModelConnection{}, errors.New("model context_window_tokens is required")
	}
	if input.AuthMode != "bearer_key" && input.AuthMode != "no_auth" {
		return ModelConnection{}, fmt.Errorf("model auth_mode %q is not supported", input.AuthMode)
	}
	return ModelConnection{
		ConnectionID:        connectionID,
		Protocol:            input.Protocol,
		Endpoint:            input.Endpoint,
		Model:               input.Model,
		ContextWindowTokens: input.ContextWindowTokens,
		AuthMode:            input.AuthMode,
		Capabilities: GatewayCapabilities{
			PromptCacheKey:        input.Protocol == "responses",
			CachedTokensUsage:     true,
			ExplicitBreakpoints:   false,
			CacheRetention:        false,
			WebsocketContinuation: false,
		},
	}, nil
}

func writeModelConnection(root string, connection ModelConnection) error {
	document := modelConnectionDocument{
		SchemaVersion: 1,
		Data: modelConnectionConfig{
			SchemaVersion:       3,
			ConnectionID:        connection.ConnectionID,
			Protocol:            connection.Protocol,
			Endpoint:            connection.Endpoint,
			Model:               connection.Model,
			ContextWindowTokens: connection.ContextWindowTokens,
			AuthMode:            connection.AuthMode,
			Capabilities: storedGatewayCapabilities{
				PromptCacheKey:        connection.Capabilities.PromptCacheKey,
				CachedTokensUsage:     connection.Capabilities.CachedTokensUsage,
				ExplicitBreakpoints:   connection.Capabilities.ExplicitBreakpoints,
				CacheRetention:        connection.Capabilities.CacheRetention,
				WebsocketContinuation: connection.Capabilities.WebsocketContinuation,
			},
		},
	}
	data, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("serializing model connection: %w", err)
	}
	filename := filepath.Join(root, modelConnectionPath)
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		return fmt.Errorf("creating model connection directory: %w", err)
	}
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		return fmt.Errorf("writing model connection: %w", err)
	}
	return nil
}

func newID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	data[6] = (data[6] & 0x0f) | 0x40
	data[8] = (data[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", data[0:4], data[4:6], data[6:8], data[8:10], data[10:16])
}
