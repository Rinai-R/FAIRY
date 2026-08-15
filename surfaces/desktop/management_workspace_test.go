package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagementWorkspacePersistsTaskAndTraceAcrossHide(t *testing.T) {
	service := NewCoreService()
	useTempProfile(t, service)
	window := &fakeWindow{x: 40, y: 80, width: 1280, height: 800}
	service.attachManagement(window)

	saved, err := service.SaveManagementWorkspaceState(ManagementWorkspaceWrite{
		Section: "tracing", TraceID: "trace-keep-this", MessageID: "message-keep-this", LogLevel: "warn", PluginInstanceID: "echo-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Section != "tracing" || saved.TraceID != "trace-keep-this" {
		t.Fatalf("saved = %#v", saved)
	}

	if err := service.OpenManagement(); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeOwnedRuntime{}
	service.edge = runtime
	if err := service.CloseManagement(); err != nil {
		t.Fatal(err)
	}
	if !window.hidden {
		t.Fatal("CloseManagement() did not hide the window")
	}
	if events := runtime.snapshot(); len(events) != 0 {
		t.Fatalf("CloseManagement() stopped runtime: %v", events)
	}

	service.workspaceLoaded = false
	window.width, window.height, window.x, window.y = 0, 0, 0, 0
	if err := service.OpenManagement(); err != nil {
		t.Fatal(err)
	}
	if window.width != 1280 || window.height != 800 || window.x != 40 || window.y != 80 {
		t.Fatalf("restored layout = %dx%d @ (%d,%d)", window.width, window.height, window.x, window.y)
	}
	state, err := service.ManagementWorkspaceState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Section != "tracing" || state.TraceID != "trace-keep-this" || state.MessageID != "message-keep-this" || state.LogLevel != "warn" || state.PluginInstanceID != "echo-1" {
		t.Fatalf("restored workspace = %#v", state)
	}
}

func TestManagementWorkspaceRejectsInvalidTask(t *testing.T) {
	service := NewCoreService()
	useTempProfile(t, service)
	if _, err := service.SaveManagementWorkspaceState(ManagementWorkspaceWrite{Section: "observability"}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("invalid task error = %v", err)
	}
	if _, err := service.SaveManagementWorkspaceState(ManagementWorkspaceWrite{Section: "plugins", PluginInstanceID: "../etc/passwd"}); err == nil || !strings.Contains(err.Error(), "plugin instance") {
		t.Fatalf("invalid plugin instance error = %v", err)
	}
}

func TestManagementWorkspaceRejectsCorruptFile(t *testing.T) {
	service := NewCoreService()
	useTempProfile(t, service)
	dir, err := service.resolveProfileDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, managementWorkspaceFileName)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ManagementWorkspaceState(); err == nil || !strings.Contains(err.Error(), "parsing") {
		t.Fatalf("corrupt state error = %v", err)
	}
}

func TestManagementWorkspaceStateJSONHasNoCredentials(t *testing.T) {
	state := ManagementWorkspaceState{Section: "tracing", TraceID: "trace-1", MessageID: "message-1", LogLevel: "info"}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{"apiKey", "Bearer", "FAIRY_API_TOKEN", "127.0.0.1:8787"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("workspace state contained %q: %s", forbidden, text)
		}
	}
}
