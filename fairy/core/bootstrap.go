package core

import (
	"errors"
)

// BootstrapOptions configures the Core process identity reported by /v1/status.
type BootstrapOptions struct {
	AppName     string
	CoreVersion string
}

// BootstrapStatus is returned by GET /v1/status.
type BootstrapStatus struct {
	AppName     string `json:"appName"`
	CoreVersion string `json:"coreVersion"`
}

// BootstrapService exposes immutable Core bootstrap status.
type BootstrapService struct {
	status BootstrapStatus
}

func NewBootstrapService(options BootstrapOptions) *BootstrapService {
	return &BootstrapService{status: BootstrapStatus{
		AppName:     options.AppName,
		CoreVersion: options.CoreVersion,
	}}
}

func (s *BootstrapService) Status() (BootstrapStatus, error) {
	if s == nil {
		return BootstrapStatus{}, errors.New("bootstrap service is not initialised")
	}
	if s.status.AppName == "" {
		return BootstrapStatus{}, errors.New("bootstrap status missing app name")
	}
	if s.status.CoreVersion == "" {
		return BootstrapStatus{}, errors.New("bootstrap status missing core version")
	}
	return s.status, nil
}
