//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package seekdb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestEmbeddedClientLeaseIsHeldUntilClose(t *testing.T) {
	paths, err := prepareRuntimePaths(filepath.Join(t.TempDir(), "seekdb-private"))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := acquireSeekDBClientLease(paths)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(paths.Run, seekDBClientsFile)
	contender, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(contender)
	if err := unix.Flock(contender, unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("exclusive lock while lease is active = %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(contender, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("exclusive lock after lease close: %v", err)
	}
}

func TestEmbeddedClientLeaseBlocksObserverStyleExclusiveWait(t *testing.T) {
	paths, err := prepareRuntimePaths(filepath.Join(t.TempDir(), "seekdb-private"))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := acquireSeekDBClientLease(paths)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(paths.Run, seekDBClientsFile)
	contender, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(contender)
	waited := make(chan error, 1)
	go func() {
		waited <- unix.Flock(contender, unix.LOCK_EX)
	}()
	select {
	case err := <-waited:
		t.Fatalf("observer-style exclusive wait completed while the shared lease was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("observer-style exclusive wait after lease close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("observer-style exclusive wait did not complete after lease close")
	}
}

func TestEmbeddedClientLeaseRejectsSymlink(t *testing.T) {
	paths, err := prepareRuntimePaths(filepath.Join(t.TempDir(), "seekdb-private"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(paths.Run, seekDBClientsFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireSeekDBClientLease(paths); err == nil {
		t.Fatal("symlink client lease was accepted")
	}
}

func TestEmbeddedClientLeaseIsReleasedWhenEngineStartFails(t *testing.T) {
	config := testRuntimeConfig(t)
	options := testLaunchOptions()
	options.start = func(context.Context, Config, runtimePaths) (engineSession, error) {
		return nil, errors.New("engine refused to start")
	}
	if _, err := open(t.Context(), config, options); err == nil {
		t.Fatal("open() succeeded with a failed engine start")
	}
	assertEmbeddedClientLeaseReleased(t, config.DataDir)
}

func TestRuntimeCloseReleasesEmbeddedClientLease(t *testing.T) {
	config := testRuntimeConfig(t)
	runtime, err := open(t.Context(), config, testLaunchOptions())
	if err != nil {
		t.Fatal(err)
	}
	closeCtx, cancel := context.WithTimeout(t.Context(), config.ShutdownLimit)
	defer cancel()
	if err := runtime.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	assertEmbeddedClientLeaseReleased(t, config.DataDir)
}

func TestRuntimeCloseRetainsLiveEmbeddedClientLeaseForProcessLifetime(t *testing.T) {
	config := testRuntimeConfig(t)
	options := testLaunchOptions()
	options.start = func(context.Context, Config, runtimePaths) (engineSession, error) {
		return liveEngine{}, nil
	}
	runtime, err := open(t.Context(), config, options)
	if err != nil {
		t.Fatal(err)
	}
	closeCtx, cancel := context.WithTimeout(t.Context(), config.ShutdownLimit)
	defer cancel()
	if err := runtime.Close(closeCtx); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(config.DataDir, "run", seekDBClientsFile)
	contender, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(contender)
	if err := unix.Flock(contender, unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatalf("exclusive lock after live Runtime Close = %v, want process lease to remain held", err)
	}
}

func assertEmbeddedClientLeaseReleased(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "run", seekDBClientsFile)
	contender, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(contender)
	if err := unix.Flock(contender, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("embedded client lease remains locked: %v", err)
	}
}
