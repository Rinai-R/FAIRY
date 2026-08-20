package seekdb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocateLibraryFindsFrameworksLayout(t *testing.T) {
	root := t.TempDir()
	macos := filepath.Join(root, "Contents", "MacOS")
	frameworks := filepath.Join(root, "Contents", "Frameworks")
	if err := os.MkdirAll(macos, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(frameworks, 0o700); err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(frameworks, libraryFileName())
	if err := os.WriteFile(library, []byte("lib"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(macos, "FAIRY")
	if err := os.WriteFile(executable, []byte("bin"), 0o700); err != nil {
		t.Fatal(err)
	}

	original := locateExecutable
	locateExecutable = func() (string, error) { return executable, nil }
	defer func() { locateExecutable = original }()

	got, err := LocateLibrary()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(library)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("LocateLibrary() = %q, want %q", got, want)
	}
}
