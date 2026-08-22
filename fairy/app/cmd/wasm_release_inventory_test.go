package cmd

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"fairy/runtime/wasm"
)

func TestWASMReleaseInventoryCommandSealsExplicitEmptyInventory(t *testing.T) {
	rootDir := t.TempDir()
	inventoryPath := filepath.Join(rootDir, "plugin-inventory.json")
	if err := os.WriteFile(inventoryPath, []byte("{\n  \"schemaVersion\": 1,\n  \"plugins\": []\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "plugin-release")
	root := NewRootCmd(testDependencies(&fakeClient{}))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"wasm-release-inventory",
		"--inventory", inventoryPath,
		"--root", rootDir,
		"--output", output,
	})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := wasm.VerifyInstalledReleaseInventory(t.Context(), output); err != nil {
		t.Fatal(err)
	}
}
