package config

import (
	"errors"
	"testing"

	"fairy/transport/openserp"
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

func TestWriteWebSearchSettingsRequiresCanonicalOrigin(t *testing.T) {
	root := t.TempDir()
	err := WriteWebSearchSettings(root, WebSearchSettings{SchemaVersion: 1, Enabled: true, BaseURL: "https://example.com/path"})
	if !errors.Is(err, openserp.ErrOriginInvalid) {
		t.Fatalf("WriteWebSearchSettings() error = %v, want %v", err, openserp.ErrOriginInvalid)
	}
	if err := WriteWebSearchSettings(root, WebSearchSettings{SchemaVersion: 1, Enabled: true, BaseURL: "HTTPS://Example.COM:8443/"}); err != nil {
		t.Fatal(err)
	}
	settings, err := ReadWebSearchSettings(root)
	if err != nil {
		t.Fatal(err)
	}
	if settings.BaseURL != "https://example.com:8443" {
		t.Fatalf("BaseURL = %q", settings.BaseURL)
	}
}

func TestEndpointOpenSERPOriginIgnoresDevelopmentEnvironment(t *testing.T) {
	t.Setenv(webSearchBaseURLEnv, "https://environment.example")
	if got := ResolveEndpointOpenSERPOrigin(""); got != "" {
		t.Fatalf("ResolveEndpointOpenSERPOrigin() = %q, want unavailable empty origin", got)
	}
	if got := ResolveEndpointOpenSERPOrigin(" HTTPS://Configured.Example/ "); got != "HTTPS://Configured.Example" {
		t.Fatalf("ResolveEndpointOpenSERPOrigin(configured) = %q", got)
	}
}

func TestEndpointWebSearchStatusIgnoresDevelopmentEnvironment(t *testing.T) {
	root := t.TempDir()
	if err := WriteWebSearchSettings(root, WebSearchSettings{SchemaVersion: 1, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(webSearchBaseURLEnv, "https://environment.example")
	status, err := NewConfigService(root, nil).EndpointWebSearchStatus()
	if err != nil {
		t.Fatal(err)
	}
	if status.BaseURL != "" || status.Ready {
		t.Fatalf("endpoint status = %#v", status)
	}
}

func TestEndpointWebSearchStatusRequiresSavedOrigin(t *testing.T) {
	root := t.TempDir()
	t.Setenv(webSearchBaseURLEnv, "https://environment.example")
	status, err := NewConfigService(root, nil).EndpointWebSearchStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Enabled || status.BaseURL != "" || status.Ready {
		t.Fatalf("endpoint status = %#v, want enabled but explicitly unavailable", status)
	}
}

func TestSaveEndpointWebSearchSettingsIgnoresDevelopmentEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv(webSearchBaseURLEnv, "https://environment.example")
	status, err := NewConfigService(root, nil).SaveEndpointWebSearchSettings(WebSearchSettings{
		Enabled: false,
		BaseURL: "https://saved.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.Ready || status.BaseURL != "https://saved.example" {
		t.Fatalf("endpoint status = %#v", status)
	}
}
