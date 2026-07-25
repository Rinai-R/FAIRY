package bootstrap

import "errors"

// StatusOptions configures the Core process identity reported by /v1/status.
type StatusOptions struct {
	AppName                string
	MigrationStage         string
	CoreVersion            string
	RespondRuntimeMigrated bool
}

// StatusService exposes immutable Core bootstrap status.
type StatusService struct {
	status Status
}

// Status is returned by GET /v1/status.
type Status struct {
	AppName                string `json:"appName"`
	MigrationStage         string `json:"migrationStage"`
	CoreVersion            string `json:"coreVersion"`
	RespondRuntimeMigrated bool   `json:"respondRuntimeMigrated"`
}

func NewStatusService(options StatusOptions) *StatusService {
	return &StatusService{
		status: Status{
			AppName:                options.AppName,
			MigrationStage:         options.MigrationStage,
			CoreVersion:            options.CoreVersion,
			RespondRuntimeMigrated: options.RespondRuntimeMigrated,
		},
	}
}

func (s *StatusService) Snapshot() (Status, error) {
	if s == nil {
		return Status{}, errors.New("bootstrap service is not initialised")
	}
	if s.status.AppName == "" {
		return Status{}, errors.New("bootstrap status missing app name")
	}
	if s.status.MigrationStage == "" {
		return Status{}, errors.New("bootstrap status missing migration stage")
	}
	if s.status.CoreVersion == "" {
		return Status{}, errors.New("bootstrap status missing core version")
	}
	return s.status, nil
}
