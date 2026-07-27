package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopProductionArtifactsDoNotReferenceSystemCredentialStore(t *testing.T) {
	tokens := []string{
		"key" + "chain",
		"github.com/keybase/go-" + "keychain",
		"com.rinai.fairy." + "macos",
		"core-api-" + "token",
	}
	roots := []string{"go.mod", "go.sum", "frontend/src", "frontend/bindings", "frontend/dist", "."}
	seen := make(map[string]bool)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path == "bin" || path == "frontend/node_modules" || strings.HasPrefix(path, "frontend/node_modules/") {
					return filepath.SkipDir
				}
				return nil
			}
			if seen[path] || !isDesktopProductionArtifact(path) {
				return nil
			}
			seen[path] = true
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			lower := strings.ToLower(string(content))
			for _, token := range tokens {
				if strings.Contains(lower, strings.ToLower(token)) {
					t.Errorf("%s contains forbidden system credential-store marker %q", path, token)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", root, err)
		}
	}
}

func isDesktopProductionArtifact(path string) bool {
	clean := filepath.Clean(path)
	if clean == "go.mod" || clean == "go.sum" {
		return true
	}
	if filepath.Dir(clean) == "." && strings.HasSuffix(clean, ".go") {
		return !strings.HasSuffix(clean, "_test.go")
	}
	if strings.HasPrefix(clean, filepath.Join("frontend", "src")+string(filepath.Separator)) {
		return !strings.Contains(filepath.Base(clean), ".test.")
	}
	return strings.HasPrefix(clean, filepath.Join("frontend", "bindings")+string(filepath.Separator)) ||
		strings.HasPrefix(clean, filepath.Join("frontend", "dist")+string(filepath.Separator))
}
