package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fairy/plugin"
)

func TestPluginValidateAndPackDoNotCreateClient(t *testing.T) {
	dir := t.TempDir()
	manifest := plugin.Manifest{
		SchemaVersion: 1, ID: "fairy.plugin.example", Version: "1.0.0",
		ABI: plugin.ABIRange{Min: 1, Max: 1}, Entry: plugin.EntryModule, Exports: plugin.RequiredExports(),
		ConfigSchemaVersion: 1, DataSchemaVersion: 1,
	}
	raw, err := plugin.EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, plugin.PackageManifestName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, plugin.PackageModuleName), []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}
	factories := 0
	deps := testDependencies(&fakeClient{})
	deps.ClientFactory = func(ConnectionConfig) (APIClient, error) {
		factories++
		return &fakeClient{}, nil
	}
	output := new(bytes.Buffer)
	root := NewRootCmd(deps)
	root.SetOut(output)
	root.SetErr(output)
	root.SetArgs([]string{"plugin", "validate", dir})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if factories != 0 {
		t.Fatalf("validate created %d clients", factories)
	}
	if !strings.Contains(output.String(), `"id":"fairy.plugin.example"`) || strings.Contains(output.String(), "sk-live") {
		t.Fatalf("validate output = %s", output.String())
	}

	archive := filepath.Join(t.TempDir(), "example.fairy-plugin")
	output.Reset()
	root = NewRootCmd(deps)
	root.SetOut(output)
	root.SetErr(output)
	root.SetArgs([]string{"plugin", "pack", dir, "--output", archive})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if factories != 0 {
		t.Fatalf("pack created %d clients", factories)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatal(err)
	}
}
