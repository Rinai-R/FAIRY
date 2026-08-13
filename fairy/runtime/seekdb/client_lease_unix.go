//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package seekdb

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const seekDBClientsFile = "seekdb.clients"

func acquireSeekDBClientLease(paths runtimePaths) (io.Closer, error) {
	path := filepath.Join(paths.Run, seekDBClientsFile)
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o644)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, errors.New("embedded client lease must not be a symlink")
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open embedded client lease")
	}
	closeOnError := func(err error) (io.Closer, error) {
		return nil, errors.Join(err, file.Close())
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return closeOnError(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return closeOnError(errors.New("embedded client lease must be a regular, singly-linked file"))
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return closeOnError(errors.New("embedded client lease must be owned by the current user"))
	}
	if stat.Mode&0o022 != 0 {
		return closeOnError(fmt.Errorf("embedded client lease permissions %04o are writable by other users", stat.Mode&0o777))
	}
	if err := unix.Flock(fd, unix.LOCK_SH|unix.LOCK_NB); err != nil {
		return closeOnError(err)
	}
	return file, nil
}
