package runtime

import "testing"

func TestBootstrapServiceStatus(t *testing.T) {
	service := NewBootstrapService(BootstrapOptions{
		AppName:                "FAIRY",
		MigrationStage:         "session-core",
		CoreVersion:            "0.1.0",
		RespondRuntimeMigrated: true,
	})
	status, err := service.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.AppName != "FAIRY" || status.MigrationStage != "session-core" || !status.RespondRuntimeMigrated {
		t.Fatalf("status = %#v", status)
	}
}

func TestBootstrapServiceRejectsIncomplete(t *testing.T) {
	cases := []BootstrapOptions{
		{MigrationStage: "session-core", CoreVersion: "0.1.0"},
		{AppName: "FAIRY", CoreVersion: "0.1.0"},
		{AppName: "FAIRY", MigrationStage: "session-core"},
	}
	for _, options := range cases {
		if _, err := NewBootstrapService(options).Status(); err == nil {
			t.Fatalf("options %#v: want error", options)
		}
	}
}
