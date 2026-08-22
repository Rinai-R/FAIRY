//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const focusRequestTimeout = 200 * time.Millisecond

type unixInstanceLock struct {
	file      *os.File
	listener  net.Listener
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func acquireInstanceLock(dir string, onFocus func()) (instanceGuard, error) {
	if err := ensureProfileDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, instanceLockName)
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, errors.New("user profile write lock must not be a symlink")
		}
		return nil, fmt.Errorf("open user profile write lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open user profile write lock")
	}
	closeOnError := func(err error) (instanceGuard, error) {
		return nil, errors.Join(err, file.Close())
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return closeOnError(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return closeOnError(errors.New("user profile write lock must be a regular, singly-linked file"))
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return closeOnError(errors.New("user profile write lock must be owned by the current user"))
	}
	if stat.Mode&0o022 != 0 {
		return closeOnError(fmt.Errorf("user profile write lock permissions %04o are writable by other users", stat.Mode&0o777))
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return closeOnError(ErrInstanceHeld)
		}
		return closeOnError(fmt.Errorf("acquire user profile write lock: %w", err))
	}
	lock := &unixInstanceLock{file: file, done: make(chan struct{})}
	listener, err := listenFocusSocket(focusSocketPath(dir))
	if err == nil {
		lock.listener = listener
		go lock.serveFocus(onFocus)
	} else {
		close(lock.done)
		lock.done = nil
	}
	return lock, nil
}

func (l *unixInstanceLock) serveFocus(onFocus func()) {
	defer close(l.done)
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			return
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		_, _ = io.Copy(io.Discard, io.LimitReader(conn, 64))
		_ = conn.Close()
		if onFocus != nil {
			onFocus()
		}
	}
}

func (l *unixInstanceLock) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		if l.listener != nil {
			l.closeErr = errors.Join(l.closeErr, l.listener.Close())
		}
		if l.done != nil {
			<-l.done
		}
		if l.file != nil {
			l.closeErr = errors.Join(l.closeErr, l.file.Close())
			l.file = nil
		}
	})
	return l.closeErr
}

func listenFocusSocket(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove stale focus socket: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, errors.Join(err, listener.Close())
	}
	return listener, nil
}

func requestInstanceFocus(dir string) error {
	conn, err := net.DialTimeout("unix", focusSocketPath(dir), focusRequestTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(focusRequestTimeout))
	_, err = conn.Write([]byte("focus\n"))
	return err
}
