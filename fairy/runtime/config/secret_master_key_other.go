//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris && !windows

package config

import (
	"fmt"
	"os"
)

func validatePrivatePermissions(os.FileMode, os.FileMode) error {
	return unsupportedMasterKeyPlatform("platform permission validation is not implemented")
}

func openMasterKeyNoFollow(string) (*os.File, error) {
	return nil, unsupportedMasterKeyPlatform("platform no-follow file opening is not implemented")
}

func validateOpenedMasterKey(*os.File, os.FileInfo, bool) error {
	return unsupportedMasterKeyPlatform("platform file ownership validation is not implemented")
}

func syncPrivateDirectory(string) error {
	return unsupportedMasterKeyPlatform("platform directory durability is not implemented")
}

func unsupportedMasterKeyPlatform(reason string) error {
	return fmt.Errorf("%w: %s", ErrMasterKeyPermissions, reason)
}
