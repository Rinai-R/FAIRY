package desktop

import "errors"

type BootstrapOptions struct {
	AppName                string
	MigrationStage         string
	CoreVersion            string
	RespondRuntimeMigrated bool
}

type BootstrapService struct {
	status BootstrapStatus
}

type BootstrapStatus struct {
	AppName                string `json:"appName"`
	MigrationStage         string `json:"migrationStage"`
	CoreVersion            string `json:"coreVersion"`
	RespondRuntimeMigrated bool   `json:"respondRuntimeMigrated"`
}

func NewBootstrapService(options BootstrapOptions) *BootstrapService {
	return &BootstrapService{
		status: BootstrapStatus{
			AppName:                options.AppName,
			MigrationStage:         options.MigrationStage,
			CoreVersion:            options.CoreVersion,
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
	if s.status.CoreVersion == "" {
		return BootstrapStatus{}, errors.New("bootstrap status missing core version")
	}
	return s.status, nil
}
