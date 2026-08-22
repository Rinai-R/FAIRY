package edge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRuntimeVerificationRootAcceptsOnlyPrivateEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeVerificationRoot(root); err != nil {
		t.Fatalf("private empty directory: %v", err)
	}

	t.Run("relative", func(t *testing.T) {
		if err := validateRuntimeVerificationRoot("relative"); err == nil {
			t.Fatal("relative verification root was accepted")
		}
	})
	t.Run("nonempty", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "unexpected"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateRuntimeVerificationRoot(root); err == nil || !strings.Contains(err.Error(), "must be empty") {
			t.Fatalf("nonempty verification root error = %v", err)
		}
	})
	t.Run("wide permissions", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := validateRuntimeVerificationRoot(root); err == nil || !strings.Contains(err.Error(), "wider than 0700") {
			t.Fatalf("wide verification root error = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "verification-root")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := validateRuntimeVerificationRoot(link); err == nil || !strings.Contains(err.Error(), "non-symlink") {
			t.Fatalf("symlink verification root error = %v", err)
		}
	})
}
