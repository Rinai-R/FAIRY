package config

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSecretCipherFromDataDirCreatesAndReusesMasterKey(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "fairy-data")
	first, err := SecretCipherFromDataDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SecretCipherFromDataDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	assertSecretCiphersShareKey(t, first, second)

	keyPath := filepath.Join(dataDir, secretMasterKeyDirectory, secretMasterKeyFilename)
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != keyBytes {
		t.Fatalf("master key length = %d, want %d", len(raw), keyBytes)
	}
	clear(raw)

	entries, err := os.ReadDir(filepath.Dir(keyPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != secretMasterKeyFilename {
		t.Fatalf("secret directory entries = %v, want only %q", entryNames(entries), secretMasterKeyFilename)
	}
}

func TestSecretCipherFromDataDirConcurrentCreationUsesOnePublishedKey(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "fairy-data")
	const callers = 16
	ciphers := make([]*SecretCipher, callers)
	errorsByCaller := make([]error, callers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			ciphers[index], errorsByCaller[index] = SecretCipherFromDataDir(dataDir)
		}()
	}
	close(start)
	wait.Wait()

	for index, err := range errorsByCaller {
		if err != nil {
			t.Fatalf("caller %d error = %v", index, err)
		}
		assertSecretCiphersShareKey(t, ciphers[0], ciphers[index])
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, secretMasterKeyDirectory))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != secretMasterKeyFilename {
		t.Fatalf("secret directory entries = %v, want only %q", entryNames(entries), secretMasterKeyFilename)
	}
}

func TestSecretCipherFromDataDirRejectsInvalidPathAndKeyShape(t *testing.T) {
	t.Run("relative data directory", func(t *testing.T) {
		_, err := SecretCipherFromDataDir("relative-data")
		if !errors.Is(err, ErrSecretDataDirectoryInvalid) {
			t.Fatalf("error = %v, want ErrSecretDataDirectoryInvalid", err)
		}
	})

	t.Run("unclean data directory", func(t *testing.T) {
		_, err := SecretCipherFromDataDir(t.TempDir() + string(filepath.Separator) + "fairy-data" + string(filepath.Separator) + "..")
		if !errors.Is(err, ErrSecretDataDirectoryInvalid) {
			t.Fatalf("error = %v, want ErrSecretDataDirectoryInvalid", err)
		}
	})

	t.Run("filesystem root", func(t *testing.T) {
		_, err := SecretCipherFromDataDir(filepath.VolumeName(t.TempDir()) + string(filepath.Separator))
		if !errors.Is(err, ErrSecretDataDirectoryInvalid) {
			t.Fatalf("error = %v, want ErrSecretDataDirectoryInvalid", err)
		}
	})

	t.Run("truncated master key", func(t *testing.T) {
		dataDir := privateMasterKeyFixture(t, bytes.Repeat([]byte{0x7a}, keyBytes-1))
		_, err := SecretCipherFromDataDir(dataDir)
		if !errors.Is(err, ErrMasterKeyFileInvalid) {
			t.Fatalf("error = %v, want ErrMasterKeyFileInvalid", err)
		}
	})

	t.Run("oversized master key does not leak content", func(t *testing.T) {
		key := bytes.Repeat([]byte{0xab}, keyBytes+1)
		dataDir := privateMasterKeyFixture(t, key)
		_, err := SecretCipherFromDataDir(dataDir)
		if !errors.Is(err, ErrMasterKeyFileInvalid) {
			t.Fatalf("error = %v, want ErrMasterKeyFileInvalid", err)
		}
		message := err.Error()
		if strings.Contains(message, string(key)) || strings.Contains(message, hex.EncodeToString(key)) {
			t.Fatalf("error leaked master key material: %q", message)
		}
	})

	t.Run("non-regular master key", func(t *testing.T) {
		dataDir := filepath.Join(t.TempDir(), "fairy-data")
		secretsDir := filepath.Join(dataDir, secretMasterKeyDirectory)
		if err := os.MkdirAll(filepath.Join(secretsDir, secretMasterKeyFilename), 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := SecretCipherFromDataDir(dataDir)
		if !errors.Is(err, ErrMasterKeyFileInvalid) {
			t.Fatalf("error = %v, want ErrMasterKeyFileInvalid", err)
		}
	})
}

func privateMasterKeyFixture(t *testing.T, key []byte) string {
	t.Helper()
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
	if err := os.WriteFile(filepath.Join(secretsDir, secretMasterKeyFilename), key, 0o600); err != nil {
		t.Fatal(err)
	}
	return dataDir
}

func assertSecretCiphersShareKey(t *testing.T, writer, reader *SecretCipher) {
	t.Helper()
	plaintext := []byte("master-key-reuse-probe")
	nonce, ciphertext, aad, err := writer.Seal("model", "probe", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := reader.Open("model", "probe", SecretKeyVersion, nonce, ciphertext, aad)
	if err != nil {
		t.Fatalf("opening value sealed by another local cipher: %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("opened plaintext = %q, want %q", opened, plaintext)
	}
	clear(opened)
	clear(ciphertext)
}

func entryNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
