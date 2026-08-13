package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const legacyFreeEnvironment = "FAIRY_REQUIRE_LEGACY_FREE"

type legacyDependencyBaseline struct {
	Roots []string                        `json:"roots"`
	Rules map[string]legacyDependencyRule `json:"rules"`
}

type legacyDependencyRule struct {
	Pattern  string `json:"pattern"`
	MaxFiles int    `json:"maxFiles"`
}

func TestLegacyRuntimeDependencyInventory(t *testing.T) {
	baseline := readLegacyDependencyBaseline(t)
	requireClean := os.Getenv(legacyFreeEnvironment) == "1"

	names := make([]string, 0, len(baseline.Rules))
	for name := range baseline.Rules {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		rule := baseline.Rules[name]
		hits := findLegacyDependencyFiles(t, baseline.Roots, rule.Pattern)
		t.Logf("%s (%d files): %s", name, len(hits), strings.Join(hits, ", "))
		if len(hits) > rule.MaxFiles {
			t.Errorf("legacy dependency %s grew from at most %d files to %d; migrate the new use or update the OpenSpec design before changing the baseline", name, rule.MaxFiles, len(hits))
		}
		if requireClean && len(hits) != 0 {
			t.Errorf("legacy dependency %s remains in production files: %s", name, strings.Join(hits, ", "))
		}
	}
}

func readLegacyDependencyBaseline(t *testing.T) legacyDependencyBaseline {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "legacy-runtime-dependencies.json"))
	if err != nil {
		t.Fatal(err)
	}
	var baseline legacyDependencyBaseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatal(err)
	}
	if len(baseline.Roots) == 0 || len(baseline.Rules) == 0 {
		t.Fatal("legacy runtime dependency baseline must declare roots and rules")
	}
	for name, rule := range baseline.Rules {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(rule.Pattern) == "" || rule.MaxFiles < 0 {
			t.Fatalf("invalid legacy dependency rule %q", name)
		}
	}
	return baseline
}

func findLegacyDependencyFiles(t *testing.T, roots []string, pattern string) []string {
	t.Helper()
	matcher, err := regexp.Compile("(?i)(?:" + pattern + ")")
	if err != nil {
		t.Fatalf("compile legacy dependency pattern %q: %v", pattern, err)
	}
	hits := make([]string, 0)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if shouldSkipLegacyDependencyDirectory(entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !isLegacyDependencySource(path) {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if matcher.Match(raw) {
				normalized := filepath.ToSlash(filepath.Clean(path))
				hits = append(hits, normalized)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan legacy dependency root %s: %v", root, err)
		}
	}
	slices.Sort(hits)
	return slices.Compact(hits)
}

func shouldSkipLegacyDependencyDirectory(name string) bool {
	switch name {
	case ".git", "bin", "dist", "node_modules", "testdata":
		return true
	default:
		return false
	}
}

func isLegacyDependencySource(path string) bool {
	name := filepath.Base(path)
	if strings.HasSuffix(name, "_test.go") {
		return false
	}
	if name == "go.mod" {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go", ".yml", ".yaml", ".js", ".jsx", ".ts", ".tsx":
		return true
	default:
		return false
	}
}
