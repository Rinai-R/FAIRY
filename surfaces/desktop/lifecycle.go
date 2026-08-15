package main

import (
	"context"
	"errors"
	"time"

	"fairy/app/edge"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	defaultTurnShutdownLimit    = 3 * time.Second
	defaultSurfaceShutdownLimit = 2 * time.Second
	defaultRuntimeShutdownLimit = 20 * time.Second
)

type ownedRuntime interface {
	Close(context.Context) error
	InterruptTurn(context.Context, string, string) error
	OpenSessionTransport() (sessionPlane, sessionAssets, error)
	Management() managementHost
}

type edgeAdapter struct {
	runtime *edge.Runtime
}

func (a edgeAdapter) Close(ctx context.Context) error {
	if a.runtime == nil {
		return nil
	}
	return a.runtime.Close(ctx)
}

func (a edgeAdapter) Management() managementHost {
	if a.runtime == nil {
		return nil
	}
	return a.runtime.Management()
}

type shutdownBudget struct {
	Turn    time.Duration
	Surface time.Duration
	Runtime time.Duration
}

func defaultShutdownBudget() shutdownBudget {
	return shutdownBudget{
		Turn:    defaultTurnShutdownLimit,
		Surface: defaultSurfaceShutdownLimit,
		Runtime: defaultRuntimeShutdownLimit,
	}
}

func defaultOpenEdge(ctx context.Context) (ownedRuntime, error) {
	runtime, err := edge.Open(ctx, edge.Options{})
	if err != nil {
		return nil, err
	}
	return edgeAdapter{runtime: runtime}, nil
}

func (s *CoreService) focusExistingInstance() {
	s.mu.Lock()
	companion := s.companion
	s.mu.Unlock()
	if companion == nil {
		return
	}
	companion.Show()
	companion.Focus()
}

func (s *CoreService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	if s == nil {
		return errors.New("desktop core service is unavailable")
	}
	resolve := s.profileDir
	if resolve == nil {
		resolve = desktopProfileDir
	}
	dir, err := resolve()
	if err != nil {
		return err
	}
	acquire := s.acquireLock
	if acquire == nil {
		acquire = acquireInstanceLock
	}
	guard, err := acquire(dir, s.focusExistingInstance)
	if err != nil {
		if errors.Is(err, ErrInstanceHeld) {
			notify := s.requestFocus
			if notify == nil {
				notify = requestInstanceFocus
			}
			_ = notify(dir)
		}
		return err
	}
	keepLock := false
	defer func() {
		if keepLock {
			return
		}
		_ = guard.Close()
	}()
	open := s.openEdge
	if open == nil {
		open = defaultOpenEdge
	}
	runtime, err := open(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.edge != nil || s.instance != nil {
		s.mu.Unlock()
		_ = runtime.Close(ctx)
		return errors.New("edge runtime is already started")
	}
	s.edge = runtime
	s.instance = guard
	if s.shutdownBudget.Turn == 0 {
		s.shutdownBudget = defaultShutdownBudget()
	}
	s.mu.Unlock()
	keepLock = true
	return nil
}

func (s *CoreService) ServiceShutdown() error {
	if s == nil {
		return nil
	}
	return s.shutdownOwnedRuntime(context.Background())
}

func (s *CoreService) shutdownOwnedRuntime(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	budget := s.shutdownBudget
	if budget.Turn == 0 {
		budget = defaultShutdownBudget()
	}
	runtime := s.edge
	guard := s.instance
	conversation, turnID, active := s.conversation, s.activeTurnID, s.active
	socket, cache, observation := s.socket, s.visualCache, s.observation
	logUnsub := s.logUnsub
	s.socket, s.assets, s.visualCache, s.observation, s.edge, s.instance = nil, nil, nil, nil, nil, nil
	s.active, s.activeTurnID, s.logUnsub = false, "", nil
	s.mu.Unlock()

	var errs error
	if logUnsub != nil {
		logUnsub()
	}
	if active && runtime != nil && conversation != "" && turnID != "" {
		turnCtx, cancel := context.WithTimeout(parent, budget.Turn)
		errs = errors.Join(errs, runtime.InterruptTurn(turnCtx, conversation, turnID))
		cancel()
	}
	surfaceCtx, cancelSurface := context.WithTimeout(parent, budget.Surface)
	if observation != nil {
		observation.Stop()
	}
	if socket != nil {
		errs = errors.Join(errs, socket.Close())
	}
	if cache != nil {
		errs = errors.Join(errs, cache.Close())
	}
	cancelSurface()
	_ = surfaceCtx
	if runtime != nil {
		runtimeCtx, cancelRuntime := context.WithTimeout(parent, budget.Runtime)
		errs = errors.Join(errs, runtime.Close(runtimeCtx))
		cancelRuntime()
	}
	if guard != nil {
		errs = errors.Join(errs, guard.Close())
	}
	return errs
}
