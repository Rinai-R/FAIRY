package edge

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"go.uber.org/zap"

	"fairy/app/core"
	appsession "fairy/app/session"
	"fairy/plugin"
	"fairy/runtime/observability"
	"fairy/runtime/wasm"
	api "fairy/transport/web"
)

var (
	ErrLifetimeContextRequired = errors.New("edge lifetime context is required")
	ErrPluginHostUnavailable   = errors.New("plugin host is not configured")
)

type Profile = core.Profile

const (
	ProfileFull           = core.ProfileFull
	ProfileDesktopLite    = core.ProfileDesktopLite
	ProfileEndpointStrict = core.ProfileEndpointStrict
)

type Options struct {
	ConfigRoot string
	Logger     *zap.Logger
	Profile    Profile
}

// pluginStore is defined by the Edge consumer so profile-isolation tests can
// prove that endpoint-strict never reads or mutates non-strict QQ state.
type pluginStore interface {
	Instances(context.Context) ([]plugin.InstanceRecord, error)
	Upgrades(context.Context, string) ([]plugin.UpgradeRecord, error)
	PutInstance(context.Context, plugin.InstanceRecord) error
	ConfigRefs(context.Context, string) ([]plugin.ConfigRef, error)
}

// Runtime is the Desktop-owned composition root. It starts SeekDB through Core,
// then Session, then the deny-by-default WASM host on the same process lifetime.
type Runtime struct {
	core          *core.Runtime
	sessions      *appsession.Service
	facade        *appsession.Facade
	host          *wasm.Host
	plugins       pluginStore
	logger        *zap.Logger
	qq            any
	qqCancel      context.CancelFunc
	qqHTTP        *http.Server
	openSERPClose func()

	closeOnce sync.Once
	closeErr  error
}

// OpenEndpointStrict is the production Desktop composition. It deliberately
// skips the legacy plugin inventory and never references the Web/QQ binders,
// so those optional integrations cannot enter the shipped dependency graph.
func OpenEndpointStrict(ctx context.Context, options Options) (*Runtime, error) {
	options.Profile = ProfileEndpointStrict
	runtime, err := openRuntimeBase(ctx, options)
	if err != nil {
		return nil, err
	}
	runtime.bindOpenSERP()
	return runtime, nil
}

func openRuntimeBase(ctx context.Context, options Options) (*Runtime, error) {
	if ctx == nil {
		return nil, ErrLifetimeContextRequired
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	coreRuntime, err := core.Open(coreRuntimeOptions(options))
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if keep {
			return
		}
		_ = coreRuntime.Close()
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sessions := newSessionService(coreRuntime.APIDependencies())
	if sessions == nil {
		return nil, appsession.ErrSessionUnavailable
	}
	host, err := wasm.OpenWith(ctx, wasm.Observer{
		Spans:   coreRuntime.Messages,
		Metrics: observability.NewPluginMetrics(),
		Logs:    coreRuntime.Logs,
	})
	if err != nil {
		return nil, err
	}
	keepHost := false
	defer func() {
		if keepHost {
			return
		}
		_ = host.Close(ctx)
	}()
	runtime := &Runtime{
		core:     coreRuntime,
		sessions: sessions,
		facade:   appsession.NewFacade(sessions),
		host:     host,
		logger:   coreRuntime.Logger,
	}
	keep = true
	keepHost = true
	return runtime, nil
}

func coreRuntimeOptions(options Options) core.RuntimeOptions {
	return core.RuntimeOptions{
		ConfigRoot: options.ConfigRoot,
		Logger:     options.Logger,
		Profile:    options.Profile,
	}
}

func newSessionService(deps *api.Dependencies) *appsession.Service {
	if deps == nil {
		return nil
	}
	return appsession.New(appsession.Dependencies{
		Secret:                 deps.Secret,
		Characters:             deps.Character,
		Transcript:             deps.TranscriptStore,
		Turns:                  deps.Turns,
		Initiative:             deps.Initiative,
		Captures:               deps.Captures,
		SubscribeTurnEvents:    deps.SubscribeTurnEvents,
		SubscribeParticipation: deps.SubscribeParticipation,
	})
}

func (r *Runtime) Core() *core.Runtime {
	if r == nil {
		return nil
	}
	return r.core
}

func (r *Runtime) Session() *appsession.Service {
	if r == nil {
		return nil
	}
	return r.sessions
}

func (r *Runtime) Facade() *appsession.Facade {
	if r == nil {
		return nil
	}
	return r.facade
}

func (r *Runtime) PluginHost() (*wasm.Host, error) {
	if r == nil || r.host == nil {
		return nil, ErrPluginHostUnavailable
	}
	return r.host, nil
}

func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.closeOnce.Do(func() {
		if r.openSERPClose != nil {
			r.openSERPClose()
			r.openSERPClose = nil
		}
		if r.qqCancel != nil {
			r.qqCancel()
			r.qqCancel = nil
		}
		if r.qqHTTP != nil {
			_ = r.qqHTTP.Close()
			r.qqHTTP = nil
		}
		if r.host != nil {
			r.closeErr = errors.Join(r.closeErr, r.host.Close(ctx))
			r.host = nil
		}
		if r.facade != nil {
			r.closeErr = errors.Join(r.closeErr, r.facade.Close())
		}
		if r.core != nil {
			r.closeErr = errors.Join(r.closeErr, r.core.Close())
		}
	})
	return r.closeErr
}
