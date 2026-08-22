package main

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type fakeInstanceGuard struct {
	closed atomic.Int32
}

func (f *fakeInstanceGuard) Close() error {
	f.closed.Add(1)
	return nil
}

func TestServiceStartupAcquiresProfileLockBeforeEdge(t *testing.T) {
	var opened atomic.Int32
	guard := &fakeInstanceGuard{}
	service := NewCoreService()
	useTempProfile(t, service)
	service.acquireLock = func(string, func()) (instanceGuard, error) {
		if opened.Load() != 0 {
			t.Error("edge opened before the profile lock was acquired")
		}
		return guard, nil
	}
	service.openEdge = func(context.Context) (ownedRuntime, error) {
		opened.Add(1)
		return &fakeOwnedRuntime{}, nil
	}
	if err := service.ServiceStartup(t.Context(), application.ServiceOptions{}); err != nil {
		t.Fatal(err)
	}
	if opened.Load() != 1 {
		t.Fatalf("edge opens = %d, want 1", opened.Load())
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	if guard.closed.Load() != 1 {
		t.Fatalf("lock closes = %d, want 1", guard.closed.Load())
	}
}

func TestServiceStartupRejectsSecondInstanceWithoutOpeningEdge(t *testing.T) {
	var opened atomic.Int32
	var focused atomic.Int32
	service := NewCoreService()
	useTempProfile(t, service)
	service.acquireLock = func(string, func()) (instanceGuard, error) {
		return nil, ErrInstanceHeld
	}
	service.requestFocus = func(string) error {
		focused.Add(1)
		return nil
	}
	service.openEdge = func(context.Context) (ownedRuntime, error) {
		opened.Add(1)
		return &fakeOwnedRuntime{}, nil
	}
	if err := service.ServiceStartup(t.Context(), application.ServiceOptions{}); !errors.Is(err, ErrInstanceHeld) {
		t.Fatalf("ServiceStartup() = %v, want %v", err, ErrInstanceHeld)
	}
	if opened.Load() != 0 {
		t.Fatal("second instance opened an edge runtime")
	}
	if focused.Load() != 1 {
		t.Fatal("second instance did not request focus on the running runtime")
	}
	service.mu.Lock()
	runtime, lock := service.edge, service.instance
	service.mu.Unlock()
	if runtime != nil || lock != nil {
		t.Fatal("rejected startup retained runtime or lock")
	}
}

func TestServiceStartupReleasesLockWhenEdgeOpenFails(t *testing.T) {
	guard := &fakeInstanceGuard{}
	service := NewCoreService()
	useTempProfile(t, service)
	service.acquireLock = func(string, func()) (instanceGuard, error) {
		return guard, nil
	}
	service.openEdge = func(context.Context) (ownedRuntime, error) {
		return nil, errors.New("seekdb binary is required")
	}
	if err := service.ServiceStartup(t.Context(), application.ServiceOptions{}); err == nil {
		t.Fatal("expected startup failure")
	}
	if guard.closed.Load() != 1 {
		t.Fatalf("failed startup lock closes = %d, want 1", guard.closed.Load())
	}
}

func TestDesktopProfileDirUsesConfigRoot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FAIRY_CONFIG_ROOT", dir)
	got, err := desktopProfileDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("desktopProfileDir() = %q, want %q", got, dir)
	}
}

func useTempProfile(t *testing.T, service *CoreService) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	service.profileDir = func() (string, error) { return dir, nil }
	service.acquireLock = func(string, func()) (instanceGuard, error) {
		return &fakeInstanceGuard{}, nil
	}
}
