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

func (s *CoreService) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	if s == nil {
		return errors.New("desktop core service is unavailable")
	}
	open := s.openEdge
	if open == nil {
		open = defaultOpenEdge
	}
	runtime, err := open(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.edge != nil {
		s.mu.Unlock()
		_ = runtime.Close(ctx)
		return errors.New("edge runtime is already started")
	}
	s.edge = runtime
	if s.shutdownBudget.Turn == 0 {
		s.shutdownBudget = defaultShutdownBudget()
	}
	s.mu.Unlock()
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
	conversation, turnID, active := s.conversation, s.activeTurnID, s.active
	socket, cache, observation := s.socket, s.visualCache, s.observation
	s.socket, s.assets, s.visualCache, s.observation, s.edge = nil, nil, nil, nil, nil
	s.active, s.activeTurnID = false, ""
	s.mu.Unlock()

	var errs error
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
	return errs
}
