package bootstrap

import "testing"

func TestStatusServiceSnapshot(t *testing.T) {
	service := NewStatusService(StatusOptions{
		AppName:                "FAIRY",
		MigrationStage:         "session-core",
		CoreVersion:            "0.1.0",
		RespondRuntimeMigrated: true,
	})
	status, err := service.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if status.AppName != "FAIRY" || status.MigrationStage != "session-core" || !status.RespondRuntimeMigrated {
		t.Fatalf("status = %#v", status)
	}
}

func TestStatusServiceRejectsIncomplete(t *testing.T) {
	cases := []StatusOptions{
		{MigrationStage: "session-core", CoreVersion: "0.1.0"},
		{AppName: "FAIRY", CoreVersion: "0.1.0"},
		{AppName: "FAIRY", MigrationStage: "session-core"},
	}
	for _, options := range cases {
		if _, err := NewStatusService(options).Snapshot(); err == nil {
			t.Fatalf("options %#v: want error", options)
		}
	}
}
