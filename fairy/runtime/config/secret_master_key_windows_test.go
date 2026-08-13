//go:build windows

package config

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSecretCipherFromDataDirFailsClosedWithoutWindowsACLValidation(t *testing.T) {
	_, err := SecretCipherFromDataDir(filepath.Join(t.TempDir(), "fairy-data"))
	if !errors.Is(err, ErrMasterKeyPermissions) {
		t.Fatalf("error = %v, want ErrMasterKeyPermissions", err)
	}
}
