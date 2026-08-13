//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package seekdb

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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

func TestEmbeddedClientLeaseIsReleasedWhenProcessStartFails(t *testing.T) {
	config := testRuntimeConfig(t)
	options := testLaunchOptions(t, "block")
	options.command = func(context.Context, Config, runtimePaths, io.Writer) *exec.Cmd {
		return exec.Command(filepath.Join(t.TempDir(), "missing-seekdb"))
	}
	options.database = func(Config, string) (*sql.DB, error) {
		return sql.OpenDB(testSQLConnector{}), nil
	}
	if _, err := open(t.Context(), config, options); err == nil {
		t.Fatal("open() succeeded with a missing process")
	}
	assertEmbeddedClientLeaseReleased(t, config.DataDir)
}

func TestRuntimeCloseReleasesEmbeddedClientLease(t *testing.T) {
	config := testRuntimeConfig(t)
	runtime, err := open(t.Context(), config, testLaunchOptions(t, "block"))
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
