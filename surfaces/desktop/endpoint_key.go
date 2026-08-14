package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	endpointKeyFileName      = "desktop.endpoint.key"
	legacyConnectionVendor   = "dev.rinai.fairy"
	legacyConnectionApp      = "desktop"
	legacyConnectionRev      = "v1"
	legacyConnectionFileName = "connection.json"
	maxEndpointKeyFileSize   = 256
	maxLegacyConnectionSize  = 64 << 10
)

var installationKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func defaultLegacyConnectionFile() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve legacy Desktop connection path: %w", err)
	}
	return filepath.Join(configDir, legacyConnectionVendor, legacyConnectionApp, legacyConnectionRev, legacyConnectionFileName), nil
}

func generateInstallationKey() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "macos-" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func loadOrCreateEndpointKey(profileDir, legacyPath string) (string, error) {
	if err := ensureProfileDir(profileDir); err != nil {
		return "", err
	}
	path := filepath.Join(profileDir, endpointKeyFileName)
	key, found, err := readEndpointKeyFile(path)
	if err != nil {
		return "", err
	}
	if !found {
		migrated, ok := migrateLegacyEndpointKey(legacyPath)
		if ok {
			key = migrated
		} else {
			key, err = generateInstallationKey()
			if err != nil {
				return "", err
			}
		}
		if err := writeEndpointKeyFile(path, key); err != nil {
			return "", err
		}
	}
	if err := removeLegacyConnectionFile(legacyPath); err != nil {
		return "", err
	}
	return key, nil
}

func readEndpointKeyFile(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("inspect Desktop installation identity file: %w", err)
	}
	if err := validateEndpointKeyFileInfo(path, info); err != nil {
		return "", false, err
	}
	if info.Size() > maxEndpointKeyFileSize {
		return "", false, errors.New("Desktop installation identity file exceeds the size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false, fmt.Errorf("open Desktop installation identity file: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", false, fmt.Errorf("inspect opened Desktop installation identity file: %w", err)
	}
	if !os.SameFile(info, opened) {
		return "", false, errors.New("Desktop installation identity file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxEndpointKeyFileSize+1))
	if err != nil {
		return "", false, fmt.Errorf("read Desktop installation identity file: %w", err)
	}
	if int64(len(raw)) > maxEndpointKeyFileSize {
		return "", false, errors.New("Desktop installation identity file exceeds the size limit")
	}
	key := strings.TrimSuffix(string(raw), "\n")
	if !installationKeyPattern.MatchString(key) {
		return "", false, errors.New("Desktop installation identity is invalid")
	}
	return key, true, nil
}

func writeEndpointKeyFile(path, key string) error {
	if !installationKeyPattern.MatchString(key) {
		return errors.New("Desktop installation identity is invalid")
	}
	directory := filepath.Dir(path)
	if err := ensureProfileDir(directory); err != nil {
		return err
	}
	if target, err := os.Lstat(path); err == nil {
		if err := validateEndpointKeyFileInfo(path, target); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect Desktop installation identity file: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".desktop.endpoint.key-*.tmp")
	if err != nil {
		return fmt.Errorf("create Desktop installation identity temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		_ = temporary.Close()
		if keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("restrict Desktop installation identity temporary file permissions: %w", err)
	}
	if _, err := temporary.WriteString(key + "\n"); err != nil {
		return fmt.Errorf("write Desktop installation identity file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync Desktop installation identity temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Desktop installation identity temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish Desktop installation identity file: %w", err)
	}
	keepTemporary = false
	written, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect published Desktop installation identity file: %w", err)
	}
	return validateEndpointKeyFileInfo(path, written)
}

func validateEndpointKeyFileInfo(path string, info fs.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("Desktop installation identity path is not a regular file")
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("Desktop installation identity file %q must use mode 0600", path)
	}
	return validateEndpointKeyOwner(path, info)
}

func migrateLegacyEndpointKey(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", false
	}
	if info.Size() <= 0 || info.Size() > maxLegacyConnectionSize {
		return "", false
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	var payload struct {
		EndpointKey string `json:"endpointKey"`
	}
	if err := json.NewDecoder(io.LimitReader(file, maxLegacyConnectionSize)).Decode(&payload); err != nil {
		return "", false
	}
	if !installationKeyPattern.MatchString(payload.EndpointKey) {
		return "", false
	}
	return payload.EndpointKey, true
}

func removeLegacyConnectionFile(path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect legacy Desktop connection file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove legacy Desktop connection symlink: %w", err)
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("legacy Desktop connection path %q is not a regular file", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove legacy Desktop connection file: %w", err)
	}
	return nil
}
