package config

import "fairy/notify"

type ConfigService struct {
	root string
}

func NewConfigService(root string) *ConfigService {
	return &ConfigService{root: root}
}

func (s *ConfigService) ModelStatus() (ModelConnectionStatus, error) {
	return ReadModelConnectionStatus(s.root)
}

func (s *ConfigService) SaveModelConnection(input ModelConnectionInput, apiKey *string) (ModelConnectionStatus, error) {
	status, err := SaveModelConnection(s.root, input, apiKey)
	if err != nil {
		return ModelConnectionStatus{}, err
	}
	notify.Emit(notify.ModelChanged(status.Configured, status.Configured))
	return status, nil
}

func (s *ConfigService) ClearModelConnection() (ModelConnectionStatus, error) {
	if _, err := ClearModelConnection(s.root); err != nil {
		return ModelConnectionStatus{}, err
	}
	status, err := ReadModelConnectionStatus(s.root)
	if err != nil {
		return ModelConnectionStatus{}, err
	}
	notify.Emit(notify.ModelChanged(status.Configured, status.Configured))
	return status, nil
}
