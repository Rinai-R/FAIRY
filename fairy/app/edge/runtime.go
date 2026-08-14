package edge

import (
	"context"
	"errors"
	"sync"

	"go.uber.org/zap"

	"fairy/app/core"
	appsession "fairy/app/session"
	api "fairy/transport/web"
)

var (
	ErrLifetimeContextRequired = errors.New("edge lifetime context is required")
	ErrPluginHostUnavailable   = errors.New("plugin host is not configured")
)

type Options struct {
	ConfigRoot string
	Logger     *zap.Logger
}

// Runtime is the Desktop-owned composition root. It starts SeekDB through Core,
// then Session, and keeps the in-process facade on the same contracts as HTTP.
// The WASM plugin host is intentionally absent until task 9.1.
type Runtime struct {
	core     *core.Runtime
	sessions *appsession.Service
	facade   *appsession.Facade
	logger   *zap.Logger

	closeOnce sync.Once
	closeErr  error
}

func Open(ctx context.Context, options Options) (*Runtime, error) {
	if ctx == nil {
		return nil, ErrLifetimeContextRequired
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	coreRuntime, err := core.Open(core.RuntimeOptions{ConfigRoot: options.ConfigRoot, Logger: options.Logger})
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
	runtime := &Runtime{
		core:     coreRuntime,
		sessions: sessions,
		facade:   appsession.NewFacade(sessions),
		logger:   coreRuntime.Logger,
	}
	keep = true
	return runtime, nil
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

func (r *Runtime) PluginHost() error {
	return ErrPluginHostUnavailable
}

func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	_ = ctx
	r.closeOnce.Do(func() {
		if r.facade != nil {
			r.closeErr = errors.Join(r.closeErr, r.facade.Close())
		}
		if r.core != nil {
			r.closeErr = errors.Join(r.closeErr, r.core.Close())
		}
	})
	return r.closeErr
}
