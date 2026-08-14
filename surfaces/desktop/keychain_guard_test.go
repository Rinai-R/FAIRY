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

func TestDesktopProductionArtifactsDoNotReferenceRemoteCoreConnection(t *testing.T) {
	tokens := []string{
		"FAIRY_API_TOKEN",
		"127.0.0.1:8787",
		"SaveConnection",
		"ConnectionSettings",
		"defaultCoreEndpoint",
	}
	err := walkDesktopProductionArtifacts(func(path string, content []byte) error {
		text := string(content)
		for _, token := range tokens {
			if strings.Contains(text, token) {
				t.Errorf("%s contains forbidden remote Core marker %q", path, token)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDesktopProductionDoesNotListenOnNonLoopbackTCP(t *testing.T) {
	forbidden := []string{
		`net.Listen("tcp"`,
		`net.Listen("tcp4"`,
		`net.Listen("tcp6"`,
		"net.ListenTCP",
		"0.0.0.0",
		":8787",
	}
	err := walkDesktopProductionArtifacts(func(path string, content []byte) error {
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		text := string(content)
		for _, token := range forbidden {
			if strings.Contains(text, token) {
				t.Errorf("%s contains forbidden non-loopback listen marker %q", path, token)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func walkDesktopProductionArtifacts(visit func(string, []byte) error) error {
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
			return visit(path, content)
		})
		if err != nil {
			return err
		}
	}
	return nil
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
