package desktop

import "testing"

func TestBootstrapServiceStatus(t *testing.T) {
	service := NewBootstrapService(BootstrapOptions{
		AppName:                "FAIRY",
		MigrationStage:         "wails3-bootstrap",
		WailsVersion:           "v3.0.0-alpha2.117",
		LegacyTauriPreserved:   true,
		RespondRuntimeMigrated: false,
	})

	status, err := service.Status()
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.AppName != "FAIRY" {
		t.Fatalf("AppName = %q, want FAIRY", status.AppName)
	}
	if status.MigrationStage != "wails3-bootstrap" {
		t.Fatalf("MigrationStage = %q, want wails3-bootstrap", status.MigrationStage)
	}
	if status.WailsVersion != "v3.0.0-alpha2.117" {
		t.Fatalf("WailsVersion = %q, want v3.0.0-alpha2.117", status.WailsVersion)
	}
	if !status.LegacyTauriPreserved {
		t.Fatal("LegacyTauriPreserved = false, want true")
	}
	if status.RespondRuntimeMigrated {
		t.Fatal("RespondRuntimeMigrated = true, want false during bootstrap stage")
	}
}

func TestBootstrapServiceRejectsIncompleteStatus(t *testing.T) {
	tests := []struct {
		name    string
		options BootstrapOptions
	}{
		{name: "missing app name", options: BootstrapOptions{MigrationStage: "stage", WailsVersion: "v3"}},
		{name: "missing migration stage", options: BootstrapOptions{AppName: "FAIRY", WailsVersion: "v3"}},
		{name: "missing wails version", options: BootstrapOptions{AppName: "FAIRY", MigrationStage: "stage"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewBootstrapService(tt.options)
			if _, err := service.Status(); err == nil {
				t.Fatal("Status() error = nil, want validation error")
			}
		})
	}
}
