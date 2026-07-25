package runtime

import (
	"errors"

	"fairy/internal/bootstrap"
)

type (
	BootstrapOptions = bootstrap.StatusOptions
	BootstrapStatus  = bootstrap.Status
)

// BootstrapService exposes immutable Core bootstrap status via the runtime facade.
type BootstrapService struct {
	inner *bootstrap.StatusService
}

func NewBootstrapService(options BootstrapOptions) *BootstrapService {
	return &BootstrapService{inner: bootstrap.NewStatusService(options)}
}

func (s *BootstrapService) Status() (BootstrapStatus, error) {
	if s == nil || s.inner == nil {
		return BootstrapStatus{}, errors.New("bootstrap service is not initialised")
	}
	return s.inner.Snapshot()
}
