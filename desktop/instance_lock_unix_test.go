//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestInstanceLockRejectsSecondWriterUntilRelease(t *testing.T) {
	dir := lockedProfileDir(t)
	first, err := acquireInstanceLock(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireInstanceLock(dir, nil); !errors.Is(err, ErrInstanceHeld) {
		t.Fatalf("second lock = %v, want %v", err, ErrInstanceHeld)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireInstanceLock(dir, nil)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInstanceLockRecoversFromStaleFileAfterCrash(t *testing.T) {
	dir := lockedProfileDir(t)
	first, err := acquireInstanceLock(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, instanceLockName)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	contender, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(contender)
	if err := unix.Flock(contender, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatalf("stale lock file remained exclusive: %v", err)
	}
	_ = unix.Flock(contender, unix.LOCK_UN)
	recovered, err := acquireInstanceLock(dir, nil)
	if err != nil {
		t.Fatalf("crash recovery lock: %v", err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInstanceLockRejectsSymlink(t *testing.T) {
	dir := lockedProfileDir(t)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, instanceLockName)); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireInstanceLock(dir, nil); err == nil {
		t.Fatal("symlink lock was accepted")
	}
}

func TestInstanceLockNotifiesExistingInstanceToFocus(t *testing.T) {
	dir := lockedProfileDir(t)
	focused := make(chan struct{}, 1)
	first, err := acquireInstanceLock(dir, func() { focused <- struct{}{} })
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	deadline := time.Now().Add(time.Second)
	for {
		if err := requestInstanceFocus(dir); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("focus socket never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-focused:
	case <-time.After(time.Second):
		t.Fatal("existing instance did not receive focus request")
	}
}

func TestInstanceLockSerializesConcurrentAcquires(t *testing.T) {
	dir := lockedProfileDir(t)
	var mu sync.Mutex
	held := 0
	maxHeld := 0
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			lock, err := acquireInstanceLock(dir, nil)
			if err != nil {
				return
			}
			mu.Lock()
			held++
			if held > maxHeld {
				maxHeld = held
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			held--
			mu.Unlock()
			_ = lock.Close()
		})
	}
	wg.Wait()
	if maxHeld != 1 {
		t.Fatalf("concurrent writers held = %d, want 1", maxHeld)
	}
}

func lockedProfileDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
