package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestPackRoundTripMatchesOpenBundle(t *testing.T) {
	manifest := validManifest()
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	var packed bytes.Buffer
	if err := Pack(&packed, manifest, module); err != nil {
		t.Fatal(err)
	}
	bundle, err := OpenBundle(bytes.NewReader(packed.Bytes()), int64(packed.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.ID != manifest.ID || !bytes.Equal(bundle.Module, module) {
		t.Fatalf("bundle = %#v", bundle)
	}
	sum := sha256.Sum256(module)
	if hex.EncodeToString(bundle.SHA256[:]) != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest = %x", bundle.SHA256)
	}
}

func TestValidatePathAcceptsDirectoryAndRejectsSecretfulManifest(t *testing.T) {
	dir := t.TempDir()
	manifest := validManifest()
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	raw, err := EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, PackageManifestName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, PackageModuleName), module, 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := ValidatePath(dir)
	if err != nil || bundle.Manifest.ID != manifest.ID {
		t.Fatalf("ValidatePath(dir) = (%#v, %v)", bundle, err)
	}
	output := filepath.Join(t.TempDir(), "example.fairy-plugin")
	packed, err := PackDir(dir, output)
	if err != nil || packed.Manifest.ID != manifest.ID {
		t.Fatalf("PackDir() = (%#v, %v)", packed, err)
	}
	if _, err := ValidatePath(output); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, PackageManifestName), []byte(`{"schemaVersion":1,"id":"fairy.plugin.example","version":"1.0.0","abi":{"min":1,"max":1},"entry":"module.wasm","exports":["fairy_alloc","fairy_free","fairy_init","fairy_handle","fairy_shutdown"],"capabilities":[],"configSchemaVersion":1,"dataSchemaVersion":1,"apiKey":"sk-live"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = ValidatePath(dir)
	if err == nil {
		t.Fatal("secretful manifest was accepted")
	}
	if bytes.Contains([]byte(err.Error()), []byte("sk-live")) {
		t.Fatalf("validate echoed secret: %v", err)
	}
}
