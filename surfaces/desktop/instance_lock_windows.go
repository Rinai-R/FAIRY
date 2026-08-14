//go:build windows

package main

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type windowsInstanceLock struct {
	handle     windows.Handle
	overlapped windows.Overlapped
}

func acquireInstanceLock(dir string, _ func()) (instanceGuard, error) {
	if err := ensureProfileDir(dir); err != nil {
		return nil, err
	}
	path, err := windows.UTF16PtrFromString(filepath.Join(dir, instanceLockName))
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open user profile write lock: %w", err)
	}
	lock := &windowsInstanceLock{handle: handle}
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, math.MaxUint32, math.MaxUint32, &lock.overlapped); err != nil {
		_ = windows.CloseHandle(handle)
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, ErrInstanceHeld
		}
		return nil, fmt.Errorf("acquire user profile write lock: %w", err)
	}
	return lock, nil
}

func (l *windowsInstanceLock) Close() error {
	if l == nil || l.handle == windows.InvalidHandle {
		return nil
	}
	handle := l.handle
	l.handle = windows.InvalidHandle
	return errors.Join(
		windows.UnlockFileEx(handle, 0, math.MaxUint32, math.MaxUint32, &l.overlapped),
		windows.CloseHandle(handle),
	)
}

func requestInstanceFocus(string) error {
	return errors.New("instance focus is not available on this platform")
}
