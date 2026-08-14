package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeDatabaseOperations struct {
	calls []string
	err   error
}

func (f *fakeDatabaseOperations) record(name string) (any, error) {
	f.calls = append(f.calls, name)
	return map[string]any{"operation": name, "ok": f.err == nil, "storage": "seekdb"}, f.err
}

func (f *fakeDatabaseOperations) Migrate(context.Context) (any, error) {
	return f.record("migrate")
}

func (f *fakeDatabaseOperations) Status(context.Context) (any, error) {
	return f.record("status")
}

func TestDatabaseCommandsUseFreshRootAndCaptureOutput(t *testing.T) {
	tests := []struct {
		args     []string
		wantCall string
	}{
		{args: []string{"db", "migrate"}, wantCall: "migrate"},
		{args: []string{"db", "status", "--output", "table"}, wantCall: "status"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			operations := &fakeDatabaseOperations{}
			deps := testDependencies(&fakeClient{})
			deps.Database = operations
			output := new(bytes.Buffer)
			root := NewRootCmd(deps)
			root.SetOut(output)
			root.SetErr(output)
			root.SetArgs(tt.args)
			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(operations.calls) != 1 || operations.calls[0] != tt.wantCall {
				t.Fatalf("calls = %v", operations.calls)
			}
			if !strings.Contains(output.String(), tt.wantCall) {
				t.Fatalf("output = %q", output.String())
			}
			if strings.Contains(strings.ToLower(output.String()), "postgres") {
				t.Fatalf("output mentioned postgres: %q", output.String())
			}
		})
	}
}

func TestDatabaseCommandValidationAndErrors(t *testing.T) {
	operations := &fakeDatabaseOperations{err: errors.New("database unavailable")}
	deps := testDependencies(&fakeClient{})
	deps.Database = operations
	root := NewRootCmd(deps)
	root.SetArgs([]string{"db", "status"})
	if err := root.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("operation error = %v", err)
	}

	help := new(bytes.Buffer)
	root = NewRootCmd(deps)
	root.SetOut(help)
	root.SetErr(help)
	root.SetArgs([]string{"db", "--help"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	encoded := strings.ToLower(help.String())
	for _, forbidden := range []string{"vector", "postgres", "pgvector", "gorm"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("db help contains %q: %s", forbidden, help)
		}
	}
}
