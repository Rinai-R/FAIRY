package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDatabaseConfigRootDefaultsToSessionCore(t *testing.T) {
	root, err := (localDatabaseOperations{getenv: func(string) string { return "" }}).configRoot()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(mustUserHomeDir(t), "Library", "Application Support", "dev.rinai.fairy", "session-core", "v1")
	if root != want {
		t.Fatalf("config root = %q, want %q", root, want)
	}
}

func mustUserHomeDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return home
}

type fakeDatabaseOperations struct {
	calls    []string
	pageSize int
	apply    bool
	err      error
}

func (f *fakeDatabaseOperations) record(name string) (any, error) {
	f.calls = append(f.calls, name)
	return map[string]any{"operation": name, "ok": f.err == nil}, f.err
}

func (f *fakeDatabaseOperations) Migrate(context.Context) (any, error) {
	return f.record("migrate")
}

func (f *fakeDatabaseOperations) Status(context.Context) (any, error) {
	return f.record("status")
}

func (f *fakeDatabaseOperations) VectorMigrate(context.Context) (any, error) {
	return f.record("vector-migrate")
}

func (f *fakeDatabaseOperations) VectorRebuild(_ context.Context, pageSize int) (any, error) {
	f.pageSize = pageSize
	return f.record("vector-rebuild")
}

func (f *fakeDatabaseOperations) VectorReconcile(_ context.Context, apply bool) (any, error) {
	f.apply = apply
	return f.record("vector-reconcile")
}

func TestDatabaseCommandsUseFreshRootAndCaptureOutput(t *testing.T) {
	tests := []struct {
		args      []string
		wantCall  string
		wantApply bool
		wantPage  int
	}{
		{args: []string{"db", "migrate"}, wantCall: "migrate"},
		{args: []string{"db", "status", "--output", "table"}, wantCall: "status"},
		{args: []string{"db", "vector", "migrate"}, wantCall: "vector-migrate"},
		{args: []string{"db", "vector", "rebuild", "--page-size", "37"}, wantCall: "vector-rebuild", wantPage: 37},
		{args: []string{"db", "vector", "reconcile"}, wantCall: "vector-reconcile"},
		{args: []string{"db", "vector", "reconcile", "--apply"}, wantCall: "vector-reconcile", wantApply: true},
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
			if operations.apply != tt.wantApply || operations.pageSize != tt.wantPage {
				t.Fatalf("apply=%v pageSize=%d", operations.apply, operations.pageSize)
			}
			if !strings.Contains(output.String(), tt.wantCall) {
				t.Fatalf("output = %q", output.String())
			}
		})
	}
}

func TestDatabaseCommandValidationAndErrors(t *testing.T) {
	operations := &fakeDatabaseOperations{}
	deps := testDependencies(&fakeClient{})
	deps.Database = operations
	root := NewRootCmd(deps)
	root.SetArgs([]string{"db", "vector", "rebuild", "--page-size", "0"})
	if err := root.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "page-size") {
		t.Fatalf("invalid page-size error = %v", err)
	}
	if len(operations.calls) != 0 {
		t.Fatalf("invalid input called operations: %v", operations.calls)
	}
	root = NewRootCmd(deps)
	root.SetArgs([]string{"db", "vector", "rebuild", "--page-size", "101"})
	if err := root.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "page-size") {
		t.Fatalf("oversized page-size error = %v", err)
	}

	operations = &fakeDatabaseOperations{err: errors.New("database unavailable")}
	deps.Database = operations
	root = NewRootCmd(deps)
	root.SetArgs([]string{"db", "status"})
	if err := root.ExecuteContext(context.Background()); err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("operation error = %v", err)
	}
}
