package config

import "errors"

type ConfigService struct {
	root    string
	secrets *SecretStore
}

func NewConfigService(root string, secrets *SecretStore) *ConfigService {
	return &ConfigService{root: root, secrets: secrets}
}

func (s *ConfigService) ModelStatus() (ModelConnectionStatus, error) {
	return ReadModelConnectionStatus(s.root)
}

func (s *ConfigService) SaveModelConnection(input ModelConnectionInput, apiKey *string) (ModelConnectionStatus, error) {
	status, err := SaveModelConnection(s.root, input, apiKey, s.secrets)
	if err != nil {
		return ModelConnectionStatus{}, err
	}
	return status, nil
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

func (s *ConfigService) QQOneBotSettings() (QQOneBotSettings, error) {
	return ReadQQOneBotSettings(s.root)
}

func (s *ConfigService) SaveQQOneBotSettings(settings QQOneBotSettings) (QQOneBotSettings, error) {
	return WriteQQOneBotSettings(s.root, settings)
}

func (s *ConfigService) SemanticEmbeddingStatus() (SemanticEmbeddingStatus, error) {
	settings, err := ReadSemanticEmbeddingSettings(s.root)
	if err != nil {
		return SemanticEmbeddingStatus{}, err
	}
	status := SemanticEmbeddingStatusFromSettings(settings)
	if !settings.Enabled || settings.LegacyReason != "" {
		return status, nil
	}
	if settings.ConnectionID == "" {
		status.Configured = false
		status.Reason = "semantic_embedding_credential_required"
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
	if !ok {
		status.Reason = "semantic_embedding_credential_required"
	}
	return status, nil
}

func (s *ConfigService) SaveSemanticEmbeddingSettings(input SemanticEmbeddingSettings, apiKey *string) (SemanticEmbeddingStatus, error) {
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
	} else {
		_, ok, err := s.secrets.Load(normalized.ConnectionID)
		if err != nil {
			return SemanticEmbeddingStatus{}, err
		}
		if !ok {
			return SemanticEmbeddingStatus{}, errors.New("semantic embedding provider requires API key")
		}
	}

	if err := WriteSemanticEmbeddingSettings(s.root, normalized); err != nil {
		if normalized.ConnectionID != "" && normalized.ConnectionID != existing.ConnectionID && s.secrets != nil {
			_ = s.secrets.Delete(normalized.ConnectionID)
		}
		restoreExistingSecret()
		return SemanticEmbeddingStatus{}, err
	}
	return s.SemanticEmbeddingStatus()
}

func (s *ConfigService) DeleteSemanticEmbeddingCredential() (SemanticEmbeddingStatus, error) {
	settings, err := ReadSemanticEmbeddingSettings(s.root)
	if err != nil {
		return SemanticEmbeddingStatus{}, err
	}
	if settings.ConnectionID == "" {
		return s.SemanticEmbeddingStatus()
	}
	if s.secrets == nil {
		return SemanticEmbeddingStatus{}, errors.New("semantic embedding secret store is required")
	}
	if err := s.secrets.Delete(settings.ConnectionID); err != nil {
		return SemanticEmbeddingStatus{}, err
	}
	return s.SemanticEmbeddingStatus()
}
