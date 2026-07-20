package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRootHelpExposesOnlyExplicitOfflineCommands(t *testing.T) {
	root := newRootCmd()
	output := new(bytes.Buffer)
	root.SetOut(output)
	root.SetErr(output)
	root.SetArgs([]string{"--help"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"preflight", "run"} {
		if !strings.Contains(output.String(), command) {
			t.Fatalf("help missing %s: %s", command, output)
		}
	}
	for _, forbidden := range []string{"api-key", "password", "master-key"} {
		if strings.Contains(strings.ToLower(output.String()), forbidden) {
			t.Fatalf("help exposes secret flag %s: %s", forbidden, output)
		}
	}
}

func TestRunRequiresExplicitIntelligencePath(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"run"})
	if err := root.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "intelligence") {
		t.Fatalf("missing source error = %v", err)
	}
}
