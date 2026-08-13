//go:build windows

package config

import (
	"fmt"
	"os"
)

// Windows does not expose owner-only ACLs through os.FileMode. Until FAIRY has
// a native ACL validator, local master-key storage must fail closed instead of
// silently trusting a potentially shared profile directory.
func validatePrivatePermissions(os.FileMode, os.FileMode) error {
	return unsupportedMasterKeyPlatform("Windows ACL validation is not implemented")
}

func openMasterKeyNoFollow(string) (*os.File, error) {
	return nil, unsupportedMasterKeyPlatform("Windows no-follow file opening is not implemented")
}

func validateOpenedMasterKey(*os.File, os.FileInfo, bool) error {
	return unsupportedMasterKeyPlatform("Windows file ownership validation is not implemented")
}

func syncPrivateDirectory(string) error {
	return unsupportedMasterKeyPlatform("Windows directory durability is not implemented")
}

func unsupportedMasterKeyPlatform(reason string) error {
	return fmt.Errorf("%w: %s", ErrMasterKeyPermissions, reason)
}
