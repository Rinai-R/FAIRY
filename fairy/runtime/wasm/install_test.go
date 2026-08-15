package wasm

import (
	"errors"
	"testing"

	"fairy/plugin"
)

func TestShadowHealthAcceptsABIGuestAndRejectsEmptyModule(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(t.Context()) })
	if err := host.ShadowHealth(t.Context(), "echo-shadow", echoGuestWASM()); err != nil {
		t.Fatal(err)
	}
	if err := host.ShadowHealth(t.Context(), "empty-shadow", emptyModule); !errors.Is(err, plugin.ErrManifestInvalid) {
		t.Fatalf("ShadowHealth(empty) = %v, want %v", err, plugin.ErrManifestInvalid)
	}
}

func TestNewInstallerRequiresHostAndStore(t *testing.T) {
	host, err := Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close(t.Context()) })
	if _, err := NewInstaller(nil, nil); !errors.Is(err, ErrHostClosed) {
		t.Fatalf("NewInstaller(nil host) = %v", err)
	}
	if _, err := NewInstaller(host, nil); !errors.Is(err, ErrInstallerStoreRequired) {
		t.Fatalf("NewInstaller(nil store) = %v", err)
	}
}
