package desktop

import "testing"

func TestBootstrapStatus(t *testing.T) {
	service := NewBootstrapService(BootstrapOptions{
		AppName:                "FAIRY",
		MigrationStage:         "session-core",
		CoreVersion:            "0.1.0",
		RespondRuntimeMigrated: true,
	})
	status, err := service.Status()
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.AppName != "FAIRY" {
		t.Fatalf("AppName = %q", status.AppName)
	}
	if status.MigrationStage != "session-core" {
		t.Fatalf("MigrationStage = %q, want session-core", status.MigrationStage)
	}
	if status.CoreVersion != "0.1.0" {
		t.Fatalf("CoreVersion = %q", status.CoreVersion)
	}
	if !status.RespondRuntimeMigrated {
		t.Fatal("RespondRuntimeMigrated = false")
	}
}

func TestBootstrapStatusRequiresFields(t *testing.T) {
	cases := []struct {
		name    string
		options BootstrapOptions
	}{
		{name: "missing app name", options: BootstrapOptions{MigrationStage: "stage", CoreVersion: "0.1.0"}},
		{name: "missing migration stage", options: BootstrapOptions{AppName: "FAIRY", CoreVersion: "0.1.0"}},
		{name: "missing core version", options: BootstrapOptions{AppName: "FAIRY", MigrationStage: "stage"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewBootstrapService(tc.options).Status()
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
