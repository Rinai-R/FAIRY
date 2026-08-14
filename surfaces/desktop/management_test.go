package main

import (
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestOpenManagementShowsAndFocusesExistingWindow(t *testing.T) {
	service := NewCoreService()
	workspace := &fakeWindow{}
	service.attachManagement(workspace)

	if err := service.OpenManagement(); err != nil {
		t.Fatalf("OpenManagement() error = %v", err)
	}
	if !workspace.shown || !workspace.focused || !service.managementOpen {
		t.Fatalf("management shown=%t focused=%t open=%t, want all true", workspace.shown, workspace.focused, service.managementOpen)
	}

	workspace.shown, workspace.focused = false, false
	if err := service.OpenManagement(); err != nil {
		t.Fatalf("second OpenManagement() error = %v", err)
	}
	if !workspace.shown || !workspace.focused {
		t.Fatal("second OpenManagement() did not focus the existing window")
	}
}

func TestCloseManagementHidesWindowWithoutRequiringCoreEndpoint(t *testing.T) {
	service := NewCoreService()
	workspace := &fakeWindow{visible: true}
	service.attachManagement(workspace)
	if err := service.OpenManagement(); err != nil {
		t.Fatal(err)
	}
	if err := service.CloseManagement(); err != nil {
		t.Fatalf("CloseManagement() error = %v", err)
	}
	if !workspace.hidden || service.managementOpen {
		t.Fatalf("management hidden=%t open=%t, want hidden and closed", workspace.hidden, service.managementOpen)
	}
}

func TestOpenManagementFailsClosedWhenWindowMissing(t *testing.T) {
	service := NewCoreService()
	err := service.OpenManagement()
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("OpenManagement() error = %v, want unavailable", err)
	}
}

func TestManagementWindowOptionsAreResizableAndLocal(t *testing.T) {
	if managementWidth < managementMinWidth || managementHeight < managementMinHeight {
		t.Fatalf("default management size %dx%d is below minimum %dx%d", managementWidth, managementHeight, managementMinWidth, managementMinHeight)
	}
	if managementMinWidth != 960 || managementMinHeight != 640 {
		t.Fatalf("management minimum = %dx%d, want 960x640", managementMinWidth, managementMinHeight)
	}
	options := application.WebviewWindowOptions{
		Width: managementWidth, Height: managementHeight,
		MinWidth: managementMinWidth, MinHeight: managementMinHeight,
	}
	if options.DisableResize {
		t.Fatal("management window must remain resizable")
	}
}
