package config

import (
	"fairy/notify"
	"fairy/secret"
)

type ConfigService struct {
	root    string
	secrets *secret.Store
	emit    notify.ConfigEmitter
}

func NewConfigService(root string, secrets *secret.Store) *ConfigService {
	return &ConfigService{root: root, secrets: secrets}
}

// AttachConfigEmitter wires configuration-change delivery from main.
func AttachConfigEmitter(s *ConfigService, emit notify.ConfigEmitter) {
	if s == nil {
		return
	}
	s.emit = emit
}

func (s *ConfigService) emitChange(change notify.ConfigurationChange) {
	if s != nil && s.emit != nil {
		s.emit(change)
	}
}

func (s *ConfigService) ModelStatus() (ModelConnectionStatus, error) {
	return ReadModelConnectionStatus(s.root)
}

func (s *ConfigService) SaveModelConnection(input ModelConnectionInput, apiKey *string) (ModelConnectionStatus, error) {
	status, err := SaveModelConnection(s.root, input, apiKey, s.secrets)
	if err != nil {
		return ModelConnectionStatus{}, err
	}
	s.emitChange(notify.ModelChanged(status.Configured, status.Configured))
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
	s.emitChange(notify.ModelChanged(status.Configured, status.Configured))
	return status, nil
}

func (s *ConfigService) SemanticEmbeddingStatus() (SemanticEmbeddingStatus, error) {
	settings, err := ReadSemanticEmbeddingSettings(s.root)
	if err != nil {
		return SemanticEmbeddingStatus{}, err
	}
	return SemanticEmbeddingStatusFromSettings(settings), nil
}

func (s *ConfigService) SaveSemanticEmbeddingSettings(input SemanticEmbeddingSettings) (SemanticEmbeddingStatus, error) {
	if err := WriteSemanticEmbeddingSettings(s.root, input); err != nil {
		return SemanticEmbeddingStatus{}, err
	}
	return s.SemanticEmbeddingStatus()
}
