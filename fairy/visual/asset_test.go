package visual

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRelativePNGPath(t *testing.T) {
	got, err := ValidateRelativePNGPath("fairy.local/images/idle.png")
	if err != nil || got != "fairy.local/images/idle.png" {
		t.Fatalf("ValidateRelativePNGPath() = %q, %v", got, err)
	}
	for _, value := range []string{
		"../idle.png",
		"fairy.local/../idle.png",
		"fairy.local/idle.webp",
		"fairy.local/idle.png?x=1",
		"/fairy.local/idle.png",
		"",
	} {
		if _, err := ValidateRelativePNGPath(value); err == nil {
			t.Fatalf("ValidateRelativePNGPath(%q) error = nil", value)
		}
	}
}

func TestResolveAssetFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fairy.local", "images", "idle.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveAssetFile(root, "fairy.local/images/idle.png")
	if err != nil {
		t.Fatalf("ResolveAssetFile() error = %v", err)
	}
	want, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ResolveAssetFile() = %q, want %q", got, want)
	}
	if _, err := ResolveAssetFile(root, "fairy.local/missing.png"); err == nil {
		t.Fatal("ResolveAssetFile(missing) error = nil")
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "fairy.local", "images", "escape.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveAssetFile(root, "fairy.local/images/escape.png"); !errors.Is(err, ErrInvalidAssetPath) {
		t.Fatalf("symlink escape error = %v", err)
	}
}
