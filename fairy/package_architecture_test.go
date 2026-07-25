package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type listedPackage struct {
	ImportPath string
	Dir        string
	Imports    []string
}

func listPackages(t *testing.T, patterns ...string) []listedPackage {
	t.Helper()
	args := append([]string{"list", "-json"}, patterns...)
	out, err := exec.Command("go", args...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, exitErr.Stderr)
		}
		t.Fatalf("go %s: %v", strings.Join(args, " "), err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(out)))
	packages := make([]listedPackage, 0)
	for {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		packages = append(packages, pkg)
	}
	return packages
}

func TestPkgPackagesAreBusinessNeutral(t *testing.T) {
	for _, pkg := range listPackages(t, "./pkg/...") {
		for _, imported := range pkg.Imports {
			if strings.HasPrefix(imported, "fairy/") && !strings.HasPrefix(imported, "fairy/pkg/") {
				t.Errorf("generic package %s imports FAIRY business package %s", pkg.ImportPath, imported)
			}
		}
	}
}

func TestInternalLayerDependenciesPointInward(t *testing.T) {
	for _, pkg := range listPackages(t, "./...") {
		if !strings.HasPrefix(pkg.ImportPath, "fairy/internal/") {
			continue
		}
		for _, imported := range pkg.Imports {
			switch {
			case strings.HasPrefix(pkg.ImportPath, "fairy/internal/domain/"):
				if strings.HasPrefix(imported, "fairy/internal/app/") || strings.HasPrefix(imported, "fairy/internal/adapters/") || strings.HasPrefix(imported, "fairy/internal/bootstrap") {
					t.Errorf("domain package %s imports outer package %s", pkg.ImportPath, imported)
				}
			case strings.HasPrefix(pkg.ImportPath, "fairy/internal/app/"):
				if strings.HasPrefix(imported, "fairy/internal/adapters/") || strings.HasPrefix(imported, "fairy/internal/bootstrap") || imported == "fairy/api" || imported == "fairy/cmd" || imported == "fairy/coreclient" {
					t.Errorf("application package %s imports outer package %s", pkg.ImportPath, imported)
				}
			}
		}
	}
}

func TestThirdPartySDKImportsMatchMigrationInventory(t *testing.T) {
	allowed := map[string][]string{
		"github.com/jackc/pgx/": {
			"fairy/postgres",
			"fairy/secret",
			"fairy/internal/adapters/memory/postgres",
		},
		"gorm.io/":                     {"fairy/postgres"},
		"github.com/qdrant/":           {"fairy/internal/adapters/memory/qdrant"},
		"github.com/openai/":           {"fairy/internal/adapters/model/openai"},
		"github.com/cloudwego/hertz/":  {"fairy/api"},
		"github.com/gorilla/websocket": {"fairy/api", "fairy/coreclient"},
		"github.com/spf13/cobra":       {"fairy/cmd"},
		"github.com/spf13/viper":       {"fairy/cmd"},
	}
	for _, pkg := range listPackages(t, "./...") {
		for _, imported := range pkg.Imports {
			for sdkPrefix, owners := range allowed {
				if strings.HasPrefix(imported, sdkPrefix) && !slices.Contains(owners, pkg.ImportPath) {
					t.Errorf("package %s imports SDK %s outside migration inventory %v", pkg.ImportPath, imported, owners)
				}
			}
		}
	}
}

func TestIndependentSurfacesDoNotImportCoreInternals(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range []string{"desktop", "macos", "qq-onebot", "turnclient"} {
		directory := filepath.Join(root, "surfaces", surface)
		if _, err := os.Stat(directory); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "node_modules" || entry.Name() == "dist" || entry.Name() == "bin" {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".go" {
				return nil
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, `"fairy/internal/`) || strings.Contains(line, `"fairy/companion`) || strings.Contains(line, `"fairy/memory`) || strings.Contains(line, `"fairy/runtime`) || strings.Contains(line, `"fairy/interaction`) {
					t.Errorf("independent Surface source %s imports Core implementation: %s", path, strings.TrimSpace(line))
				}
			}
			return scanner.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdaptersAreOrganizedByDomain(t *testing.T) {
	root := filepath.Join("internal", "adapters")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenTopLevel := map[string]bool{
		"postgres": true, "qdrant": true, "openai": true, "embedding": true,
		"http": true, "grpc": true, "sdk": true,
	}
	allowedDomains := map[string]bool{"memory": true, "model": true}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if forbiddenTopLevel[name] {
			t.Errorf("adapter technology package %s must live under adapters/<domain>/%s, not as a top-level adapter", name, name)
		}
		if !allowedDomains[name] {
			t.Errorf("unexpected top-level adapter domain %s; expected domain-first layout under %v", name, keys(allowedDomains))
		}
	}
	for domain := range allowedDomains {
		domainDir := filepath.Join(root, domain)
		if _, err := os.Stat(domainDir); err != nil {
			t.Errorf("expected adapter domain directory %s: %v", domainDir, err)
		}
	}
}

func keys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}
