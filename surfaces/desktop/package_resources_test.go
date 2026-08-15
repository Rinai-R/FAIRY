package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPackagedPluginResourcesArePresentAndDeniedByDefault(t *testing.T) {
	root := filepath.Join("build")
	defaultsRaw, err := os.ReadFile(filepath.Join(root, "plugin-host.defaults.json"))
	if err != nil {
		t.Fatal(err)
	}
	var defaults struct {
		DefaultCapabilityGrants []string `json:"defaultCapabilityGrants"`
	}
	if err := json.Unmarshal(defaultsRaw, &defaults); err != nil {
		t.Fatal(err)
	}
	if len(defaults.DefaultCapabilityGrants) != 0 {
		t.Fatalf("default grants = %#v", defaults.DefaultCapabilityGrants)
	}
	for _, relative := range []string{
		filepath.Join("..", "..", "fairy", "plugin", "schema", "manifest.v1.json"),
		filepath.Join("..", "..", "fairy", "plugin", "schema", "envelope.v1.json"),
		filepath.Join("..", "..", "fairy", "plugin", "websearch", "manifest.json"),
		filepath.Join("..", "..", "fairy", "plugin", "qqonebot", "manifest.json"),
	} {
		if _, err := os.Stat(filepath.Join(".", relative)); err != nil {
			t.Fatalf("missing packaged plugin resource %s: %v", relative, err)
		}
	}
}
