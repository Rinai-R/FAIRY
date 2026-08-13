//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSecretCipherFromDataDirCreatesOwnerOnlyUnixBoundary(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "fairy-data")
	if _, err := SecretCipherFromDataDir(dataDir); err != nil {
		t.Fatal(err)
	}
	assertUnixMode(t, dataDir, 0o700)
	assertUnixMode(t, filepath.Join(dataDir, secretMasterKeyDirectory), 0o700)
	assertUnixMode(t, filepath.Join(dataDir, secretMasterKeyDirectory, secretMasterKeyFilename), 0o600)
}

func TestSecretCipherFromDataDirRejectsUnsafeUnixBoundary(t *testing.T) {
	t.Run("wide data directory", func(t *testing.T) {
		dataDir := privateMasterKeyFixture(t, bytes.Repeat([]byte{1}, keyBytes))
		if err := os.Chmod(dataDir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := SecretCipherFromDataDir(dataDir)
		if !errors.Is(err, ErrMasterKeyPermissions) {
			t.Fatalf("error = %v, want ErrMasterKeyPermissions", err)
		}
	})

	t.Run("wide secrets directory", func(t *testing.T) {
		dataDir := privateMasterKeyFixture(t, bytes.Repeat([]byte{2}, keyBytes))
		if err := os.Chmod(filepath.Join(dataDir, secretMasterKeyDirectory), 0o750); err != nil {
			t.Fatal(err)
		}
		_, err := SecretCipherFromDataDir(dataDir)
		if !errors.Is(err, ErrMasterKeyPermissions) {
			t.Fatalf("error = %v, want ErrMasterKeyPermissions", err)
		}
	})

	t.Run("wide master key file", func(t *testing.T) {
		dataDir := privateMasterKeyFixture(t, bytes.Repeat([]byte{3}, keyBytes))
		keyPath := filepath.Join(dataDir, secretMasterKeyDirectory, secretMasterKeyFilename)
		if err := os.Chmod(keyPath, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := SecretCipherFromDataDir(dataDir)
		if !errors.Is(err, ErrMasterKeyPermissions) {
			t.Fatalf("error = %v, want ErrMasterKeyPermissions", err)
		}
	})

	t.Run("symlink master key", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "fairy-data")
		secretsDir := filepath.Join(dataDir, secretMasterKeyDirectory)
		if err := os.MkdirAll(secretsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(secretsDir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target.key")
		if err := os.WriteFile(target, bytes.Repeat([]byte{4}, keyBytes), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(secretsDir, secretMasterKeyFilename)); err != nil {
			t.Fatal(err)
		}
		_, err := SecretCipherFromDataDir(dataDir)
		if !errors.Is(err, ErrMasterKeyFileInvalid) {
			t.Fatalf("error = %v, want ErrMasterKeyFileInvalid", err)
		}
	})

	t.Run("symlink secrets directory", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "fairy-data")
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "secrets-target")
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dataDir, secretMasterKeyDirectory)); err != nil {
			t.Fatal(err)
		}
		_, err := SecretCipherFromDataDir(dataDir)
		if !errors.Is(err, ErrSecretDataDirectoryInvalid) {
			t.Fatalf("error = %v, want ErrSecretDataDirectoryInvalid", err)
		}
	})

	t.Run("hard-linked master key", func(t *testing.T) {
		dataDir := privateMasterKeyFixture(t, bytes.Repeat([]byte{5}, keyBytes))
		keyPath := filepath.Join(dataDir, secretMasterKeyDirectory, secretMasterKeyFilename)
		if err := os.Link(keyPath, filepath.Join(t.TempDir(), "shared-master.key")); err != nil {
			t.Fatal(err)
		}
		_, err := SecretCipherFromDataDir(dataDir)
		if !errors.Is(err, ErrMasterKeyFileInvalid) {
			t.Fatalf("error = %v, want ErrMasterKeyFileInvalid", err)
		}
	})
}

func assertUnixMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
}
