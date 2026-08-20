package cmd

import (
	"errors"
	"io"
	"testing"

	"fairy/runtime/seekdb"
)

func TestSeekDBArtifactCommandFailsBeforePackagingCandidate(t *testing.T) {
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
	if err := root.ExecuteContext(t.Context()); !errors.Is(err, seekdb.ErrArtifactCandidate) {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
}
