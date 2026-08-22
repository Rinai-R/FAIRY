package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const modelConnectionPath = "model/connection.json"

type ModelConnectionStatus struct {
	Configured            bool                `json:"configured"`
	Ready                 bool                `json:"ready"`
	Protocol              string              `json:"protocol,omitempty"`
	Endpoint              string              `json:"endpoint,omitempty"`
	Model                 string              `json:"model,omitempty"`
	ContextWindowTokens   uint64              `json:"contextWindowTokens,omitempty"`
	AuthMode              string              `json:"authMode,omitempty"`
	Capabilities          GatewayCapabilities `json:"capabilities,omitempty"`
	CredentialConfigured  bool                `json:"credentialConfigured"`
	SecretStorageMigrated bool                `json:"secretStorageMigrated"`
	Reason                string              `json:"reason,omitempty"`
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
	VisionInput         bool   `json:"visionInput"`
}

type GatewayCapabilities struct {
	PromptCacheKey        bool `json:"promptCacheKey"`
	CachedTokensUsage     bool `json:"cachedTokensUsage"`
	ExplicitBreakpoints   bool `json:"explicitBreakpoints"`
	CacheRetention        bool `json:"cacheRetention"`
	WebsocketContinuation bool `json:"websocketContinuation"`
	VisionInput           bool `json:"visionInput"`
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
	VisionInput           bool `json:"vision_input"`
}

func (c storedGatewayCapabilities) public() GatewayCapabilities {
	return GatewayCapabilities{
		PromptCacheKey:        c.PromptCacheKey,
		CachedTokensUsage:     c.CachedTokensUsage,
		ExplicitBreakpoints:   c.ExplicitBreakpoints,
		CacheRetention:        c.CacheRetention,
		WebsocketContinuation: c.WebsocketContinuation,
		VisionInput:           c.VisionInput,
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

func SaveModelConnection(root string, input ModelConnectionInput, apiKey *string, secrets *SecretStore) (ModelConnectionStatus, error) {
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
			value, err := NewSecretValue(*apiKey)
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
	status := statusFromConnection(connection)
	status.Ready = true
	status.CredentialConfigured = connection.AuthMode == "bearer_key"
	return status, nil
}

func ClearModelConnection(root string, secrets *SecretStore) (bool, error) {
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

func resolveSecretStore(_ string, secrets *SecretStore) (*SecretStore, error) {
	if secrets != nil {
		return secrets, nil
	}
	return nil, errors.New("model secret store is required")
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
		Ready:                 connection.AuthMode == "no_auth",
		Protocol:              connection.Protocol,
		Endpoint:              connection.Endpoint,
		Model:                 connection.Model,
		ContextWindowTokens:   connection.ContextWindowTokens,
		AuthMode:              connection.AuthMode,
		Capabilities:          connection.Capabilities,
		CredentialConfigured:  false,
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
	if config.SchemaVersion != 3 && config.SchemaVersion != 4 {
		return ModelConnection{}, fmt.Errorf("model connection schema_version = %d, want 3 or 4", config.SchemaVersion)
	}
	if config.ConnectionID == "" {
		return ModelConnection{}, errors.New("model connection_id is required")
	}
	if config.Protocol != "responses" && config.Protocol != "chat_completions" {
		return ModelConnection{}, fmt.Errorf("model protocol %q is not supported", config.Protocol)
	}
	endpoint, err := normalizeModelEndpoint(config.Endpoint)
	if err != nil {
		return ModelConnection{}, err
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
		Endpoint:            endpoint,
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
	endpoint, err := normalizeModelEndpoint(input.Endpoint)
	if err != nil {
		return ModelConnection{}, err
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
		Endpoint:            endpoint,
		Model:               input.Model,
		ContextWindowTokens: input.ContextWindowTokens,
		AuthMode:            input.AuthMode,
		Capabilities: GatewayCapabilities{
			PromptCacheKey:        input.Protocol == "responses",
			CachedTokensUsage:     true,
			ExplicitBreakpoints:   false,
			CacheRetention:        false,
			WebsocketContinuation: false,
			VisionInput:           input.VisionInput,
		},
	}, nil
}

func normalizeModelEndpoint(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", errors.New("model endpoint is required without surrounding whitespace")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return "", errors.New("model endpoint must be a valid HTTP(S) base URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", errors.New("model endpoint must be an HTTP(S) base URL without userinfo, query or fragment")
	}
	hostname := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if hostname == "" || strings.IndexFunc(hostname, func(r rune) bool { return r > 127 || r <= 32 }) >= 0 {
		return "", errors.New("model endpoint host must be non-empty ASCII")
	}
	port := parsed.Port()
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value <= 0 || value > 65535 {
			return "", errors.New("model endpoint port is invalid")
		}
		parsed.Host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	} else {
		parsed.Host = hostname
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	last := ""
	if len(segments) > 0 {
		last = segments[len(segments)-1]
	}
	if last == "responses" || last == "embeddings" || len(segments) >= 2 && segments[len(segments)-2] == "chat" && last == "completions" {
		return "", errors.New("model endpoint must be a base URL, not a protocol resource URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

func writeModelConnection(root string, connection ModelConnection) error {
	document := modelConnectionDocument{
		SchemaVersion: 1,
		Data: modelConnectionConfig{
			SchemaVersion:       4,
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
				VisionInput:           connection.Capabilities.VisionInput,
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
