package desktop

import "errors"

type BootstrapOptions struct {
	AppName                string
	MigrationStage         string
	WailsVersion           string
	RespondRuntimeMigrated bool
}

type BootstrapService struct {
	status BootstrapStatus
}

type BootstrapStatus struct {
	AppName                string `json:"appName"`
	MigrationStage         string `json:"migrationStage"`
	WailsVersion           string `json:"wailsVersion"`
	RespondRuntimeMigrated bool   `json:"respondRuntimeMigrated"`
}

func NewBootstrapService(options BootstrapOptions) *BootstrapService {
	return &BootstrapService{
		status: BootstrapStatus{
			AppName:                options.AppName,
			MigrationStage:         options.MigrationStage,
			WailsVersion:           options.WailsVersion,
			RespondRuntimeMigrated: options.RespondRuntimeMigrated,
		},
	}
}

func (s *BootstrapService) Status() (BootstrapStatus, error) {
	if s == nil {
		return BootstrapStatus{}, errors.New("bootstrap service is not initialised")
	}
	if s.status.AppName == "" {
		return BootstrapStatus{}, errors.New("bootstrap status missing app name")
	}
	if s.status.MigrationStage == "" {
		return BootstrapStatus{}, errors.New("bootstrap status missing migration stage")
	}
	if s.status.WailsVersion == "" {
		return BootstrapStatus{}, errors.New("bootstrap status missing Wails version")
	}
	return s.status, nil
}
