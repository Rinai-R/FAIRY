//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package config

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func validatePrivatePermissions(mode os.FileMode, want os.FileMode) error {
	if mode.Perm() != want || mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return fmt.Errorf("mode is not owner-only")
	}
	return nil
}

func openMasterKeyNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, ErrMasterKeyFileInvalid
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrMasterKeyFileInvalid
	}
	return file, nil
}

func validateOpenedMasterKey(_ *os.File, info os.FileInfo, requireSettledLink bool) error {
	uid, links, ok := unixFileIdentity(info)
	if !ok {
		return ErrMasterKeyFileInvalid
	}
	if uid != uint64(os.Geteuid()) {
		return fmt.Errorf("%w: master key is not owned by the current user", ErrMasterKeyPermissions)
	}
	switch links {
	case 1:
		return nil
	case 0:
		return ErrMasterKeyFileInvalid
	default:
		if !requireSettledLink {
			return nil
		}
		return errMasterKeyPublicationInProgress
	}
}

func unixFileIdentity(info os.FileInfo) (uid, links uint64, ok bool) {
	switch stat := info.Sys().(type) {
	case *unix.Stat_t:
		return uint64(stat.Uid), uint64(stat.Nlink), true
	case *syscall.Stat_t:
		return uint64(stat.Uid), uint64(stat.Nlink), true
	default:
		return 0, 0, false
	}
}

func syncPrivateDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
