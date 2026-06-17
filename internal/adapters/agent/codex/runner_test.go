package codex

import (
	"reflect"
	"testing"
)

func TestRunnerBuildArgsStartsNewSession(t *testing.T) {
	t.Parallel()

	runner := NewRunner("codex", "gpt-5-codex", ".", 0)
	args := runner.buildArgs(ExecRequest{}, "/tmp/schema.json", "/tmp/output.json")
	want := []string{
		"exec",
		"--sandbox", "read-only",
		"--output-schema", "/tmp/schema.json",
		"--output-last-message", "/tmp/output.json",
		"--json",
		"--model", "gpt-5-codex",
		"-",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestNewRunnerDefaultsToIsolatedWorkDir(t *testing.T) {
	t.Parallel()

	runner := NewRunner("", "", "", 0)
	if runner.WorkDir != DefaultWorkDir {
		t.Fatalf("WorkDir = %q, want %q", runner.WorkDir, DefaultWorkDir)
	}
}

func TestRunnerBuildArgsResumesSession(t *testing.T) {
	t.Parallel()

	runner := NewRunner("codex", "", ".", 0)
	args := runner.buildArgs(ExecRequest{SessionID: "session-123"}, "/tmp/schema.json", "/tmp/output.json")
	want := []string{
		"exec",
		"--sandbox", "read-only",
		"--output-schema", "/tmp/schema.json",
		"--output-last-message", "/tmp/output.json",
		"--json",
		"resume", "session-123", "-",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}
