package cmd

import (
	"errors"
	"io"
	"strings"
	"testing"

	"fairy/runtime/seekdb"
)

func TestSeekDBArtifactCommandRequiresCompleteVerifiedBuildInputs(t *testing.T) {
	root := NewRootCmd(testDependencies(&fakeClient{}))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{
		"seekdb-artifact",
		"--goos", "darwin",
		"--goarch", "arm64",
		"--library", "/missing/libseekdb.dylib",
		"--license", "/missing/LICENSE",
		"--notice", "/missing/NOTICE",
	})
	if err := root.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "app Info.plist build input paths are required") {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
}

func TestSeekDBArtifactCommandRejectsUnsupportedTargetBeforeBuildInputs(t *testing.T) {
	root := NewRootCmd(testDependencies(&fakeClient{}))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"seekdb-artifact", "--goos", "darwin", "--goarch", "amd64"})
	if err := root.ExecuteContext(t.Context()); !errors.Is(err, seekdb.ErrArtifactUnsupported) {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
}
