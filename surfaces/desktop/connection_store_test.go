package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFileConnectionStoreRoundTripUsesRestrictedAtomicFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop", "v1", "connection.json")
	store := fileConnectionStore{path: path}
	first := desktopConnection{
		Endpoint:    "http://127.0.0.1:8787/",
		EndpointKey: "macos-test",
		Token:       "first-token",
	}
	if err := store.Save(first); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	assertPathMode(t, filepath.Dir(path), 0o700)
	assertPathMode(t, path, 0o600)
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	first.Endpoint = "http://127.0.0.1:8787"
	if loaded != first {
		t.Fatalf("Load() = %#v, want %#v", loaded, first)
	}

	second := first
	second.Token = "replacement-token"
	if err := store.Save(second); err != nil {
		t.Fatalf("replacement Save() error = %v", err)
	}
	loaded, err = store.Load()
	if err != nil {
		t.Fatalf("replacement Load() error = %v", err)
	}
	if loaded != second {
		t.Fatalf("replacement Load() = %#v, want %#v", loaded, second)
	}
	temporary, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".connection-*.tmp"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary files remain after atomic save: %v", temporary)
	}
}

func TestFileConnectionStoreMissingIsUnconfigured(t *testing.T) {
	store := fileConnectionStore{path: filepath.Join(t.TempDir(), "missing", "connection.json")}
	if _, err := store.Load(); !errors.Is(err, errConnectionNotFound) {
		t.Fatalf("Load() error = %v, want errConnectionNotFound", err)
	}
}

func TestSystemConnectionStoreReloadsFromTemporaryUserConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	first, ok := newSystemConnectionStore().(fileConnectionStore)
	if !ok {
		t.Fatalf("newSystemConnectionStore() type = %T, want fileConnectionStore", newSystemConnectionStore())
	}
	wantPath := filepath.Join(home, "Library", "Application Support", "dev.rinai.fairy", "desktop", "v1", "connection.json")
	if first.path != wantPath {
		t.Fatalf("system connection path = %q, want %q", first.path, wantPath)
	}
	connection := desktopConnection{Endpoint: defaultCoreEndpoint, EndpointKey: "macos-restart", Token: "restart-token"}
	if err := first.Save(connection); err != nil {
		t.Fatalf("first process Save() error = %v", err)
	}

	second, ok := newSystemConnectionStore().(fileConnectionStore)
	if !ok {
		t.Fatalf("restarted newSystemConnectionStore() type = %T, want fileConnectionStore", newSystemConnectionStore())
	}
	loaded, err := second.Load()
	if err != nil {
		t.Fatalf("restarted Load() error = %v", err)
	}
	if loaded != connection {
		t.Fatalf("restarted Load() = %#v, want %#v", loaded, connection)
	}
	assertPathMode(t, filepath.Dir(wantPath), 0o700)
	assertPathMode(t, wantPath, 0o600)
}

func TestFileConnectionStoreRejectsInvalidContent(t *testing.T) {
	valid := `{"endpoint":"http://127.0.0.1:8787","endpointKey":"macos-test","token":"secret"}`
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "malformed JSON", content: `{`, want: "decode Desktop connection file"},
		{name: "unknown field", content: `{"endpoint":"http://127.0.0.1:8787","endpointKey":"macos-test","token":"secret","extra":true}`, want: "unknown field"},
		{name: "trailing JSON", content: valid + `{}`, want: "trailing JSON value"},
		{name: "empty token", content: `{"endpoint":"http://127.0.0.1:8787","endpointKey":"macos-test","token":""}`, want: "must not be empty"},
		{name: "whitespace token", content: `{"endpoint":"http://127.0.0.1:8787","endpointKey":"macos-test","token":" secret "}`, want: "surrounding whitespace"},
		{name: "invalid endpoint", content: `{"endpoint":"http://example.com","endpointKey":"macos-test","token":"secret"}`, want: "require HTTPS"},
		{name: "invalid key", content: `{"endpoint":"http://127.0.0.1:8787","endpointKey":"invalid key","token":"secret"}`, want: "installation key is invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeConnectionFixture(t, test.content)
			_, err := (fileConnectionStore{path: path}).Load()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.want)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("Load() error leaked token: %v", err)
			}
		})
	}
}

func TestFileConnectionStoreRejectsUnsafePermissionsAndType(t *testing.T) {
	t.Run("file mode", func(t *testing.T) {
		path := writeConnectionFixture(t, validConnectionFixture)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := (fileConnectionStore{path: path}).Load(); err == nil || !strings.Contains(err.Error(), "mode 0600") {
			t.Fatalf("Load() error = %v, want mode 0600", err)
		}
	})
	t.Run("directory mode", func(t *testing.T) {
		path := writeConnectionFixture(t, validConnectionFixture)
		if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := (fileConnectionStore{path: path}).Load(); err == nil || !strings.Contains(err.Error(), "mode 0700") {
			t.Fatalf("Load() error = %v, want mode 0700", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target.json")
		if err := os.WriteFile(target, []byte(validConnectionFixture), 0o600); err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(root, "desktop")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "connection.json")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := (fileConnectionStore{path: path}).Load(); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("Load() error = %v, want regular file rejection", err)
		}
	})
}

func TestValidateConnectionOwnerRejectsDifferentUser(t *testing.T) {
	info := ownerFixtureInfo{stat: &syscall.Stat_t{Uid: uint32(os.Getuid() + 1)}}
	if err := validateConnectionOwner("fixture", info); err == nil || !strings.Contains(err.Error(), "current user") {
		t.Fatalf("validateConnectionOwner() error = %v, want current user rejection", err)
	}
}

const validConnectionFixture = `{"endpoint":"http://127.0.0.1:8787","endpointKey":"macos-test","token":"secret"}`

func writeConnectionFixture(t *testing.T, content string) string {
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
