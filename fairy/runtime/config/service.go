package config

import (
	"errors"
	"sync"
)

type SemanticEmbeddingRuntime interface {
	PrepareSemanticEmbedding(SemanticEmbeddingSettings) (commit func(), err error)
	DisableSemanticEmbedding()
}

type ConfigService struct {
	root                  string
	secrets               *SecretStore
	semanticMu            sync.RWMutex
	semanticRuntime       SemanticEmbeddingRuntime
	writeSemanticSettings func(string, SemanticEmbeddingSettings) error
}

func (s *ConfigService) AttachSemanticEmbeddingRuntime(runtime SemanticEmbeddingRuntime) {
	if s == nil {
		return
	}
	s.semanticMu.Lock()
	s.semanticRuntime = runtime
	s.semanticMu.Unlock()
}

func NewConfigService(root string, secrets *SecretStore) *ConfigService {
	return &ConfigService{root: root, secrets: secrets, writeSemanticSettings: WriteSemanticEmbeddingSettings}
}

func (s *ConfigService) ModelStatus() (ModelConnectionStatus, error) {
	status, err := ReadModelConnectionStatus(s.root)
	if err != nil {
		return ModelConnectionStatus{}, err
	}
	if !status.Configured {
		status.Reason = "model_connection_required"
		return status, nil
	}
	if status.AuthMode == "no_auth" {
		status.Ready = true
		return status, nil
	}
	connection, err := ReadModelConnection(s.root)
	if err != nil {
		return ModelConnectionStatus{}, err
	}
	if s.secrets == nil {
		status.Reason = "model_secret_store_required"
		return status, nil
	}
	_, ok, err := s.secrets.Load(connection.ConnectionID)
	if err != nil {
		return ModelConnectionStatus{}, err
	}
	status.CredentialConfigured = ok
	status.Ready = ok
	if !ok {
		status.Reason = "model_credential_required"
	}
	return status, nil
}

func (s *ConfigService) SaveModelConnection(input ModelConnectionInput, apiKey *string) (ModelConnectionStatus, error) {
	_, err := SaveModelConnection(s.root, input, apiKey, s.secrets)
	if err != nil {
		return ModelConnectionStatus{}, err
	}
	return s.ModelStatus()
}

func (s *ConfigService) ClearModelConnection() (ModelConnectionStatus, error) {
	if _, err := ClearModelConnection(s.root, s.secrets); err != nil {
		return ModelConnectionStatus{}, err
	}
	status, err := ReadModelConnectionStatus(s.root)
	if err != nil {
		return ModelConnectionStatus{}, err
	}
	return status, nil
}

func (s *ConfigService) SemanticEmbeddingStatus() (SemanticEmbeddingStatus, error) {
	s.semanticMu.RLock()
	defer s.semanticMu.RUnlock()
	return s.semanticEmbeddingStatus()
}

func (s *ConfigService) semanticEmbeddingStatus() (SemanticEmbeddingStatus, error) {
	settings, err := ReadSemanticEmbeddingSettings(s.root)
	if err != nil {
		return SemanticEmbeddingStatus{}, err
	}
	status := SemanticEmbeddingStatusFromSettings(settings)
	if settings.Provider == SemanticEmbeddingProviderNone || settings.LegacyReason != "" {
		return status, nil
	}
	if settings.ConnectionID == "" {
		status.Configured = false
		if settings.Enabled {
			status.Reason = "semantic_embedding_credential_required"
		}
		return status, nil
	}
	if s.secrets == nil {
		status.Configured = false
		status.Reason = "semantic_embedding_secret_store_required"
		return status, nil
	}
	_, ok, err := s.secrets.Load(settings.ConnectionID)
	if err != nil {
		return SemanticEmbeddingStatus{}, err
	}
	status.CredentialConfigured = ok
	status.Configured = status.Configured && ok
	if settings.Enabled && !ok {
		status.Reason = "semantic_embedding_credential_required"
	}
	return status, nil
}

func (s *ConfigService) SaveSemanticEmbeddingSettings(input SemanticEmbeddingSettings, apiKey *string) (SemanticEmbeddingStatus, error) {
	s.semanticMu.Lock()
	defer s.semanticMu.Unlock()
	existing, err := ReadSemanticEmbeddingSettings(s.root)
	if err != nil {
		return SemanticEmbeddingStatus{}, err
	}
	input.SchemaVersion = semanticEmbeddingSchemaVersion
	input.ConnectionID = existing.ConnectionID
	if input.Provider != SemanticEmbeddingProviderNone && input.ConnectionID == "" {
		input.ConnectionID = "semantic_embedding." + newID()
	}
	normalized, err := normalizeSemanticEmbeddingSettings(input)
	if err != nil {
		return SemanticEmbeddingStatus{}, err
	}
	if normalized.Provider == SemanticEmbeddingProviderNone {
		normalized.ConnectionID = ""
	}
	if normalized.Provider != SemanticEmbeddingProviderNone && s.secrets == nil {
		return SemanticEmbeddingStatus{}, errors.New("semantic embedding secret store is required")
	}

	var oldSecret SecretValue
	var oldSecretOK bool
	if existing.ConnectionID != "" && s.secrets != nil {
		oldSecret, oldSecretOK, err = s.secrets.Load(existing.ConnectionID)
		if err != nil {
			return SemanticEmbeddingStatus{}, err
		}
	}
	restoreExistingSecret := func() {
		if s.secrets == nil || existing.ConnectionID == "" {
			return
		}
		if oldSecretOK {
			_ = s.secrets.Save(existing.ConnectionID, oldSecret)
		} else {
			_ = s.secrets.Delete(existing.ConnectionID)
		}
	}
	rollbackSecret := func() {
		if normalized.ConnectionID != "" && normalized.ConnectionID != existing.ConnectionID && s.secrets != nil {
			_ = s.secrets.Delete(normalized.ConnectionID)
		}
		restoreExistingSecret()
	}

	if normalized.Provider == SemanticEmbeddingProviderNone {
		if existing.ConnectionID != "" && s.secrets != nil {
			if err := s.secrets.Delete(existing.ConnectionID); err != nil {
				return SemanticEmbeddingStatus{}, err
			}
		}
	} else if apiKey != nil {
		value, err := NewSecretValue(*apiKey)
		if err != nil {
			return SemanticEmbeddingStatus{}, err
		}
		if err := s.secrets.Save(normalized.ConnectionID, value); err != nil {
			return SemanticEmbeddingStatus{}, err
		}
	} else if normalized.Enabled {
		_, ok, err := s.secrets.Load(normalized.ConnectionID)
		if err != nil {
			return SemanticEmbeddingStatus{}, err
		}
		if !ok {
			return SemanticEmbeddingStatus{}, errors.New("semantic embedding provider requires API key")
		}
	}
	commitRuntime := func() {}
	if s.semanticRuntime != nil {
		commitRuntime, err = s.semanticRuntime.PrepareSemanticEmbedding(normalized)
		if err != nil {
			rollbackSecret()
			return SemanticEmbeddingStatus{}, err
		}
		if commitRuntime == nil {
			rollbackSecret()
			return SemanticEmbeddingStatus{}, errors.New("semantic embedding runtime returned no commit")
		}
	}

	if err := s.writeSemanticSettings(s.root, normalized); err != nil {
		rollbackSecret()
		return SemanticEmbeddingStatus{}, err
	}
	commitRuntime()
	return s.semanticEmbeddingStatus()
}

func (s *ConfigService) DeleteSemanticEmbeddingCredential() (SemanticEmbeddingStatus, error) {
	s.semanticMu.Lock()
	defer s.semanticMu.Unlock()
	settings, err := ReadSemanticEmbeddingSettings(s.root)
	if err != nil {
		return SemanticEmbeddingStatus{}, err
	}
	if settings.ConnectionID == "" {
		if s.semanticRuntime != nil {
			s.semanticRuntime.DisableSemanticEmbedding()
		}
		return s.semanticEmbeddingStatus()
	}
	if s.secrets == nil {
		return SemanticEmbeddingStatus{}, errors.New("semantic embedding secret store is required")
	}
	if err := s.secrets.Delete(settings.ConnectionID); err != nil {
		return SemanticEmbeddingStatus{}, err
	}
	if s.semanticRuntime != nil {
		s.semanticRuntime.DisableSemanticEmbedding()
	}
	return s.semanticEmbeddingStatus()
}
