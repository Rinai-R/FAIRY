package core

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type fakeManagedServer struct {
	runStarted  chan struct{}
	stop        chan struct{}
	once        sync.Once
	shutdownErr error
}

func (s *fakeManagedServer) Run() error {
	close(s.runStarted)
	<-s.stop
	return nil
}

func (s *fakeManagedServer) Shutdown(context.Context) error {
	if s.shutdownErr != nil {
		return s.shutdownErr
	}
	s.once.Do(func() { close(s.stop) })
	return nil
}

func TestRunManagedStopsOnContextCancellation(t *testing.T) {
	srv := &fakeManagedServer{runStarted: make(chan struct{}), stop: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runManaged(ctx, srv) }()
	<-srv.runStarted
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunManagedPropagatesShutdownError(t *testing.T) {
	want := errors.New("shutdown failed")
	srv := &fakeManagedServer{runStarted: make(chan struct{}), stop: make(chan struct{}), shutdownErr: want}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runManaged(ctx, srv) }()
	<-srv.runStarted
	cancel()
	if err := <-done; !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	close(srv.stop)
}
