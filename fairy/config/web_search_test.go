package config

import (
	"os"
	"path/filepath"
	"testing"

	"fairy/search"
)

func TestWebSearchSettingsDefaultEnabled(t *testing.T) {
	root := t.TempDir()
	settings, err := ReadWebSearchSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled {
		t.Fatal("enabled should default true")
	}
	service := NewConfigService(root, nil)
	status, err := service.WebSearchStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || status.BinaryFound {
		t.Fatalf("status = %#v", status)
	}
	next, err := service.SetWebSearchEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if next.Enabled {
		t.Fatal("expected disabled")
	}
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, search.BinaryName()), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	status, err = service.WebSearchStatus()
	if err != nil || !status.BinaryFound {
		t.Fatalf("status = %#v err=%v", status, err)
	}
}
