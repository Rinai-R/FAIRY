//go:build windows

package seekdb

import (
	"errors"
	"io"
	"math"
	"path/filepath"

	"golang.org/x/sys/windows"
)

const seekDBClientsFile = "seekdb.clients"

type windowsClientLease struct {
	handle     windows.Handle
	overlapped windows.Overlapped
}

func acquireSeekDBClientLease(paths runtimePaths) (io.Closer, error) {
	path, err := windows.UTF16PtrFromString(filepath.Join(paths.Run, seekDBClientsFile))
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
		return nil, err
	}
	lease := &windowsClientLease{handle: handle}
	if err := windows.LockFileEx(handle, windows.LOCKFILE_FAIL_IMMEDIATELY, 0, math.MaxUint32, math.MaxUint32, &lease.overlapped); err != nil {
		return nil, errors.Join(err, windows.CloseHandle(handle))
	}
	return lease, nil
}

func (l *windowsClientLease) Close() error {
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
