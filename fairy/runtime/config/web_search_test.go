package config

import (
	"testing"
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
	if !status.Enabled || status.BaseURL == "" {
		t.Fatalf("status = %#v", status)
	}
	next, err := service.SetWebSearchEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if next.Enabled {
		t.Fatal("expected disabled")
	}
}
