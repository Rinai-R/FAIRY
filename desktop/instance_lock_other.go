//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package main

func acquireInstanceLock(string, func()) (instanceGuard, error) {
	return nil, ErrInstanceLockUnsupported
}

func requestInstanceFocus(string) error {
	return ErrInstanceLockUnsupported
}
