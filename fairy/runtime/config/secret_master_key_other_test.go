//go:build !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris && !windows

package config

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSecretCipherFromDataDirFailsClosedOnUnsupportedPlatform(t *testing.T) {
	_, err := SecretCipherFromDataDir(filepath.Join(t.TempDir(), "fairy-data"))
	if !errors.Is(err, ErrMasterKeyPermissions) {
		t.Fatalf("error = %v, want ErrMasterKeyPermissions", err)
	}
}
