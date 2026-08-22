package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLoadOrCreateEndpointKeyPersistsRestrictedFile(t *testing.T) {
	profile := t.TempDir()
	if err := os.Chmod(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := loadOrCreateEndpointKey(profile, filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("loadOrCreateEndpointKey() error = %v", err)
	}
	if !installationKeyPattern.MatchString(first) || !strings.HasPrefix(first, "macos-") {
		t.Fatalf("generated key = %q", first)
	}
	path := filepath.Join(profile, endpointKeyFileName)
	assertPathMode(t, path, 0o600)
	second, err := loadOrCreateEndpointKey(profile, "")
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if second != first {
		t.Fatalf("reloaded key = %q, want %q", second, first)
	}
}

func TestLoadOrCreateEndpointKeyMigratesLegacyConnectionAndDeletesIt(t *testing.T) {
	profile := t.TempDir()
	if err := os.Chmod(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := writeLegacyConnectionFixture(t, `{"endpoint":"http://example.invalid","endpointKey":"macos-migrated","token":"legacy-secret"}`)
	key, err := loadOrCreateEndpointKey(profile, legacy)
	if err != nil {
		t.Fatalf("loadOrCreateEndpointKey() error = %v", err)
	}
	if key != "macos-migrated" {
		t.Fatalf("migrated key = %q, want macos-migrated", key)
	}
	if _, err := os.Lstat(legacy); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("legacy connection file still present: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(profile, endpointKeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "legacy-secret") || strings.Contains(string(raw), "example.invalid") {
		t.Fatalf("identity file retained legacy secrets: %q", raw)
	}
}

func TestLoadOrCreateEndpointKeyInvalidLegacyGeneratesNewKeyAndDeletesLegacy(t *testing.T) {
	profile := t.TempDir()
	if err := os.Chmod(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := writeLegacyConnectionFixture(t, `{"endpointKey":"invalid key","token":"legacy-secret"}`)
	key, err := loadOrCreateEndpointKey(profile, legacy)
	if err != nil {
		t.Fatalf("loadOrCreateEndpointKey() error = %v", err)
	}
	if key == "invalid key" || !installationKeyPattern.MatchString(key) {
		t.Fatalf("generated key = %q", key)
	}
	if _, err := os.Lstat(legacy); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("invalid legacy connection file still present: %v", err)
	}
}

func TestReadEndpointKeyFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("macos-target\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, endpointKeyFileName)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readEndpointKeyFile(path); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("readEndpointKeyFile() error = %v, want regular file rejection", err)
	}
}

func TestReadEndpointKeyFileRejectsWidePermissions(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, endpointKeyFileName)
	if err := os.WriteFile(path, []byte("macos-wide\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readEndpointKeyFile(path); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("readEndpointKeyFile() error = %v, want 0600 rejection", err)
	}
}

func TestValidateEndpointKeyOwnerRejectsDifferentUser(t *testing.T) {
	info := ownerFixtureInfo{stat: &syscall.Stat_t{Uid: uint32(os.Getuid() + 1)}}
	if err := validateEndpointKeyOwner("fixture", info); err == nil || !strings.Contains(err.Error(), "current user") {
		t.Fatalf("validateEndpointKeyOwner() error = %v, want current user rejection", err)
	}
}

func TestRuntimeInfoReturnsLocalProfileWithoutConnectionFields(t *testing.T) {
	service := NewCoreService()
	useTempProfile(t, service)
	dir, err := service.resolveProfileDir()
	if err != nil {
		t.Fatal(err)
	}
	info, err := service.RuntimeInfo()
	if err != nil {
		t.Fatalf("RuntimeInfo() error = %v", err)
	}
	if info.ProfileDir != dir || info.Ready {
		t.Fatalf("RuntimeInfo() = %#v, want profile %q not ready", info, dir)
	}
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"endpoint"`, `"token"`, "http://", "127.0.0.1"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("RuntimeInfo JSON contained %q: %s", forbidden, encoded)
		}
	}
}

func writeLegacyConnectionFixture(t *testing.T, content string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "desktop", "v1")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "connection.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertPathMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}

type ownerFixtureInfo struct {
	stat *syscall.Stat_t
}

func (ownerFixtureInfo) Name() string       { return "fixture" }
func (ownerFixtureInfo) Size() int64        { return 0 }
func (ownerFixtureInfo) Mode() fs.FileMode  { return 0o600 }
func (ownerFixtureInfo) ModTime() time.Time { return time.Time{} }
func (ownerFixtureInfo) IsDir() bool        { return false }
func (info ownerFixtureInfo) Sys() any      { return info.stat }
