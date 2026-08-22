package edge

import (
	"strings"

	"go.uber.org/zap"

	"fairy/context/knowledge"
	"fairy/runtime/config"
	"fairy/transport/openserp"
)

// bindOpenSERP installs the endpoint-strict native search adapter. OpenSERP is
// optional: missing, disabled, or invalid settings never make the local Agent
// unavailable, and no direct-result fetch fallback is installed.
func (r *Runtime) bindOpenSERP() {
	if r == nil || r.core == nil {
		return
	}
	settings, err := config.ReadWebSearchSettings(r.core.ConfigRoot)
	if err != nil {
		r.logOpenSERPUnavailable("settings unavailable")
		return
	}
	if !settings.Enabled {
		return
	}
	origin := config.ResolveEndpointOpenSERPOrigin(settings.BaseURL)
	if origin == "" {
		r.logOpenSERPUnavailable("origin not configured")
		return
	}
	authority, err := openserp.NewAuthority(origin)
	if err != nil {
		r.logOpenSERPUnavailable("origin rejected")
		return
	}
	service := knowledge.NewWebSearchServiceWithAuthority(authority)
	knowledge.AttachWebSearchLogger(service, r.logger.Named("openserp"))
	r.core.BindWebSearch(service, service)
	r.openSERPClose = authority.Close
}

func (r *Runtime) logOpenSERPUnavailable(reason string) {
	if r == nil || r.logger == nil {
		return
	}
	r.logger.Info("optional OpenSERP capability unavailable", zap.String("reason", strings.TrimSpace(reason)))
}
