package secret

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestDatabasePathRequiresRoot(t *testing.T) {
	_, err := DatabasePath("")
	if !errors.Is(err, ErrRootRequired) {
		t.Fatalf("DatabasePath() error = %v, want %v", err, ErrRootRequired)
	}
}

func TestDatabasePathUsesExistingRelativeLocation(t *testing.T) {
	got, err := DatabasePath("/tmp/fairy")
	if err != nil {
		t.Fatalf("DatabasePath() error = %v", err)
	}
	want := filepath.Join("/tmp/fairy", "model", "secrets.sqlite3")
	if got != want {
		t.Fatalf("DatabasePath() = %q, want %q", got, want)
	}
}

func TestNewValueRejectsInvalidSecrets(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want error
	}{
		{name: "empty", raw: "", want: ErrSecretRequired},
		{name: "leading whitespace", raw: " sk-test", want: ErrInvalidSecret},
		{name: "trailing whitespace", raw: "sk-test ", want: ErrInvalidSecret},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewValue(tt.raw)
			if !errors.Is(err, tt.want) {
				t.Fatalf("NewValue() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestValueRedactsStringAndJSON(t *testing.T) {
	value, err := NewValue("sk-test-secret")
	if err != nil {
		t.Fatalf("NewValue() error = %v", err)
	}
	if fmt.Sprint(value) != "[REDACTED]" {
		t.Fatalf("String() = %q", fmt.Sprint(value))
	}
	if fmt.Sprintf("%#v", value) != "secret.Value([REDACTED])" {
		t.Fatalf("GoString() = %q", fmt.Sprintf("%#v", value))
	}
	if _, err := json.Marshal(value); err == nil {
		t.Fatal("MarshalJSON() error = nil, want explicit error")
	}
	if value.Expose() != "sk-test-secret" {
		t.Fatal("Expose() did not return the exact secret value")
	}
}

func TestStoreRequiresPath(t *testing.T) {
	_, _, err := NewStore("").Load("connection-1")
	if !errors.Is(err, ErrStorePathRequired) {
		t.Fatalf("Load() error = %v, want %v", err, ErrStorePathRequired)
	}
}

func TestStoreRejectsInvalidConnectionID(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "secrets.sqlite3"))
	value, err := NewValue("sk-test-secret")
	if err != nil {
		t.Fatalf("NewValue() error = %v", err)
	}
	tests := []struct {
		name string
		id   string
		want error
	}{
		{name: "empty", id: "", want: ErrConnectionIDRequired},
		{name: "leading whitespace", id: " connection-1", want: ErrInvalidConnectionID},
		{name: "trailing whitespace", id: "connection-1 ", want: ErrInvalidConnectionID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Save(tt.id, value); !errors.Is(err, tt.want) {
				t.Fatalf("Save() error = %v, want %v", err, tt.want)
			}
			if _, _, err := store.Load(tt.id); !errors.Is(err, tt.want) {
				t.Fatalf("Load() error = %v, want %v", err, tt.want)
			}
			if err := store.Delete(tt.id); !errors.Is(err, tt.want) {
				t.Fatalf("Delete() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestStoreSaveLoadAndDelete(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "model", "secrets.sqlite3"))
	value, err := NewValue("sk-test-secret")
	if err != nil {
		t.Fatalf("NewValue() error = %v", err)
	}

	if err := store.Save("connection-1", value); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, ok, err := store.Load("connection-1")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !ok {
		t.Fatal("Load() ok = false, want true")
	}
	if loaded.Expose() != "sk-test-secret" {
		t.Fatalf("Load() = %q", loaded.Expose())
	}

	if err := store.Delete("connection-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	_, ok, err = store.Load("connection-1")
	if err != nil {
		t.Fatalf("Load() after Delete() error = %v", err)
	}
	if ok {
		t.Fatal("Load() after Delete() ok = true, want false")
	}
}

func TestStoreSaveUpdatesExistingSecret(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "secrets.sqlite3"))
	first, err := NewValue("sk-first")
	if err != nil {
		t.Fatalf("NewValue(first) error = %v", err)
	}
	second, err := NewValue("sk-second")
	if err != nil {
		t.Fatalf("NewValue(second) error = %v", err)
	}
	if err := store.Save("connection-1", first); err != nil {
		t.Fatalf("Save(first) error = %v", err)
	}
	if err := store.Save("connection-1", second); err != nil {
		t.Fatalf("Save(second) error = %v", err)
	}
	loaded, ok, err := store.Load("connection-1")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !ok || loaded.Expose() != "sk-second" {
		t.Fatalf("Load() = (%q, %v), want updated secret", loaded.Expose(), ok)
	}
}

func TestStoreLoadMissingReturnsFalse(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "secrets.sqlite3"))
	_, ok, err := store.Load("missing-connection")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if ok {
		t.Fatal("Load() ok = true, want false")
	}
}
