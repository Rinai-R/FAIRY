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

func TestApplicationPackagesDoNotImportInternalLayers(t *testing.T) {
	for _, pkg := range listPackages(t, "./...") {
		for _, imported := range pkg.Imports {
			if strings.HasPrefix(imported, "fairy/internal/") {
				t.Errorf("application package %s imports obsolete internal layer %s", pkg.ImportPath, imported)
			}
		}
	}
}

func TestTopLevelDomainPackagesDoNotImportInfrastructure(t *testing.T) {
	domains := []string{
		"./compaction",
		"./extraction",
		"./interaction",
		"./observation",
		"./participation",
		"./persona",
		"./proactive",
		"./reply",
		"./sociallearning",
	}
	for _, pkg := range listPackages(t, domains...) {
		for _, imported := range pkg.Imports {
			if imported == "fairy/api" || imported == "fairy/cmd" || imported == "fairy/coreclient" || imported == "fairy/runtime" {
				t.Errorf("domain package %s imports infrastructure/composition package %s", pkg.ImportPath, imported)
			}
		}
	}
}

func TestMemoryPackageDoesNotImportCompanion(t *testing.T) {
	for _, pkg := range listPackages(t, "./memory") {
		for _, imported := range pkg.Imports {
			if imported == "fairy/companion" || strings.HasPrefix(imported, "fairy/companion/") {
				t.Errorf("memory owner imports companion consumer package %s", imported)
			}
		}
	}
}

func TestThirdPartySDKImportsMatchMigrationInventory(t *testing.T) {
	allowed := map[string][]string{
		"github.com/jackc/pgx/": {
			"fairy/coredb",
			"fairy/coredb/schema",
			"fairy/secret",
			"fairy/memory",
		},
		"gorm.io/":                     {"fairy/coredb/schema"},
		"github.com/qdrant/":           {"fairy/vectorindex"},
		"github.com/openai/":           {"fairy/model"},
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

func TestCoreDatabasePackagesHaveSingleOwners(t *testing.T) {
	if _, err := os.Stat("postgres"); err == nil {
		t.Fatal("obsolete top-level postgres package still exists")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat obsolete postgres package: %v", err)
	}

	packages := listPackages(t, "./coredb", "./coredb/schema")
	if len(packages) != 2 {
		t.Fatalf("core database package count = %d, want 2", len(packages))
	}
	for _, pkg := range packages {
		for _, imported := range pkg.Imports {
			switch pkg.ImportPath {
			case "fairy/coredb":
				if imported == "fairy/coredb/schema" || strings.HasPrefix(imported, "gorm.io/") {
					t.Errorf("coredb resource package imports schema owner %s", imported)
				}
			case "fairy/coredb/schema":
				if strings.HasPrefix(imported, "fairy/") {
					t.Errorf("coredb/schema imports application package %s", imported)
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
	for _, surface := range []string{"desktop", "qq-onebot", "turnclient"} {
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
