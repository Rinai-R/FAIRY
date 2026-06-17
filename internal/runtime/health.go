package runtime

import (
	"context"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/health"
)

func (r *Runtime) ProviderHealth(ctx context.Context) []health.Result {
	results := []health.Result{}
	for provider, engine := range r.agents {
		results = append(results, checkProvider(ctx, "agent", string(provider), engine))
	}
	for provider, engine := range r.voices {
		results = append(results, checkProvider(ctx, "voice", string(provider), engine))
	}
	for provider, engine := range r.images {
		results = append(results, checkProvider(ctx, "image", string(provider), engine))
	}
	for provider, engine := range r.scenes {
		results = append(results, checkProvider(ctx, "scene", string(provider), engine))
	}
	return results
}

func checkProvider(ctx context.Context, domain string, provider string, engine any) health.Result {
	checker, ok := engine.(health.Checker)
	if !ok {
		return health.Result{
			Domain:    domain,
			Provider:  provider,
			Status:    health.StatusUnknown,
			Message:   "provider 未实现健康检查",
			CheckedAt: time.Now().UTC(),
		}
	}
	result := checker.Check(ctx)
	if result.Domain == "" {
		result.Domain = domain
	}
	result.Provider = provider
	if result.CheckedAt.IsZero() {
		result.CheckedAt = time.Now().UTC()
	}
	return result
}
