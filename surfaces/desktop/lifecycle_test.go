package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type fakeOwnedRuntime struct {
	mu         sync.Mutex
	events     []string
	closeDelay time.Duration
	closeErr   error
	interrupt  error
	host       managementHost
}

func (f *fakeOwnedRuntime) Close(ctx context.Context) error {
	f.mu.Lock()
	f.events = append(f.events, "runtime")
	delay := f.closeDelay
	err := f.closeErr
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (f *fakeOwnedRuntime) OpenSessionTransport() (sessionPlane, sessionAssets, error) {
	return nil, nil, errors.New("fake runtime has no session transport")
}

func (f *fakeOwnedRuntime) Management() managementHost {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.host
}

func (f *fakeOwnedRuntime) InterruptTurn(ctx context.Context, conversationID, turnID string) error {
	f.mu.Lock()
	f.events = append(f.events, "turn:"+conversationID+":"+turnID)
	err := f.interrupt
	f.mu.Unlock()
	select {
	case <-ctx.Done():
		if err == nil {
			return ctx.Err()
		}
	default:
	}
	return err
}

func (f *fakeOwnedRuntime) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func TestServiceStartupFailsClosedWhenEdgeOpenFails(t *testing.T) {
	service := NewCoreService()
	useTempProfile(t, service)
	want := errors.New("seekdb binary is required")
	service.openEdge = func(context.Context) (ownedRuntime, error) {
		return nil, want
	}
	if err := service.ServiceStartup(t.Context(), application.ServiceOptions{}); !errors.Is(err, want) {
		t.Fatalf("ServiceStartup() error = %v", err)
	}
	service.mu.Lock()
	runtime := service.edge
	service.mu.Unlock()
	if runtime != nil {
		t.Fatal("failed startup retained an edge runtime")
	}
}

func TestServiceShutdownInterruptsActiveTurnBeforeRuntimeClose(t *testing.T) {
	runtime := &fakeOwnedRuntime{}
	service := NewCoreService()
	service.edge = runtime
	service.conversation = "conversation-1"
	service.active = true
	service.activeTurnID = "turn-9"
	if err := service.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	got := runtime.snapshot()
	if len(got) != 2 || got[0] != "turn:conversation-1:turn-9" || got[1] != "runtime" {
		t.Fatalf("shutdown order = %v, want turn then runtime", got)
	}
}

func TestServiceShutdownHonorsReverseDeadlines(t *testing.T) {
	runtime := &fakeOwnedRuntime{closeDelay: time.Second}
	service := NewCoreService()
	service.edge = runtime
	service.shutdownBudget = shutdownBudget{
		Turn:    20 * time.Millisecond,
		Surface: 20 * time.Millisecond,
		Runtime: 30 * time.Millisecond,
	}
	started := time.Now()
	err := service.shutdownOwnedRuntime(context.Background())
	elapsed := time.Since(started)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want deadline exceeded", err)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("shutdown waited %s, want bounded runtime deadline", elapsed)
	}
	if got := runtime.snapshot(); len(got) != 1 || got[0] != "runtime" {
		t.Fatalf("idle shutdown order = %v, want runtime close only", got)
	}
}

func TestServiceStartupThenShutdownClosesInjectedRuntime(t *testing.T) {
	runtime := &fakeOwnedRuntime{}
	service := NewCoreService()
	useTempProfile(t, service)
	service.openEdge = func(context.Context) (ownedRuntime, error) {
		return runtime, nil
	}
	if err := service.ServiceStartup(t.Context(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	if got := runtime.snapshot(); len(got) != 1 || got[0] != "runtime" {
		t.Fatalf("events = %v", got)
	}
	if err := service.ServiceStartup(t.Context(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestServiceStartupDoesNotMentionLegacyCoreEndpoint(t *testing.T) {
	service := NewCoreService()
	useTempProfile(t, service)
	service.openEdge = func(context.Context) (ownedRuntime, error) {
		return nil, errors.New("SeekDB binary is required")
	}
	err := service.ServiceStartup(t.Context(), application.ServiceOptions{})
	if err == nil {
		t.Fatal("expected startup failure")
	}
	message := strings.ToLower(err.Error())
	for _, forbidden := range []string{"bearer", "127.0.0.1:8787", "postgres"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("startup error mentioned %q: %v", forbidden, err)
		}
	}
}
