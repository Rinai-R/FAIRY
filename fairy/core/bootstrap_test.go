package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBootstrapServiceStatus(t *testing.T) {
	service := NewBootstrapService(BootstrapOptions{
		AppName:     "FAIRY",
		CoreVersion: "0.1.0",
	})
	status, err := service.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.AppName != "FAIRY" || status.CoreVersion != "0.1.0" {
		t.Fatalf("status = %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	if strings.Contains(value, "migrationStage") || strings.Contains(value, "respondRuntimeMigrated") {
		t.Fatalf("bootstrap status retains migration state: %s", value)
	}
}

func TestBootstrapServiceRejectsIncomplete(t *testing.T) {
	cases := []BootstrapOptions{
		{CoreVersion: "0.1.0"},
		{AppName: "FAIRY"},
	}
	for _, options := range cases {
		if _, err := NewBootstrapService(options).Status(); err == nil {
			t.Fatalf("options %#v: want error", options)
		}
	}
}
