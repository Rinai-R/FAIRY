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

var targetPackages = []string{
	"fairy",
	"fairy/api",
	"fairy/character",
	"fairy/cmd",
	"fairy/companion",
	"fairy/config",
	"fairy/core",
	"fairy/coreclient",
	"fairy/coredb",
	"fairy/desktopcapture",
	"fairy/initiative",
	"fairy/memory",
	"fairy/model",
	"fairy/observability",
	"fairy/persona",
	"fairy/reply",
	"fairy/session",
	"fairy/speech",
	"fairy/sticker",
}

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

func TestPackageInventoryMatchesConsolidatedLayout(t *testing.T) {
	got := make([]string, 0, len(targetPackages))
	for _, pkg := range listPackages(t, "./...") {
		got = append(got, pkg.ImportPath)
	}
	slices.Sort(got)
	want := slices.Clone(targetPackages)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		for _, pkg := range got {
			if !slices.Contains(want, pkg) {
				t.Errorf("unexpected package after consolidation: %s", pkg)
			}
		}
		for _, pkg := range want {
			if !slices.Contains(got, pkg) {
				t.Errorf("missing package after consolidation: %s", pkg)
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

func TestBusinessPackagesDoNotImportComposition(t *testing.T) {
	for _, pkg := range listPackages(t, "./...") {
		if slices.Contains([]string{"fairy", "fairy/api", "fairy/cmd", "fairy/core"}, pkg.ImportPath) {
			continue
		}
		for _, imported := range pkg.Imports {
			if slices.Contains([]string{"fairy/api", "fairy/cmd", "fairy/core"}, imported) {
				t.Errorf("business package %s imports composition package %s", pkg.ImportPath, imported)
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

func TestOrchestrationPackagesDoNotImportEachOther(t *testing.T) {
	for _, pkg := range listPackages(t, "./companion", "./initiative") {
		for _, imported := range pkg.Imports {
			if pkg.ImportPath == "fairy/companion" && imported == "fairy/initiative" {
				t.Error("companion imports initiative; Core must adapt the two orchestration boundaries")
			}
			if pkg.ImportPath == "fairy/initiative" && imported == "fairy/companion" {
				t.Error("initiative imports companion; TurnStarter must remain a consumption-side interface")
			}
		}
	}
}

func TestThirdPartySDKImportsMatchMigrationInventory(t *testing.T) {
	allowed := map[string][]string{
		"github.com/jackc/pgx/": {
			"fairy/coredb",
			"fairy/config",
			"fairy/memory",
			"fairy/sticker",
		},
		"gorm.io/":                     {"fairy/coredb"},
		"github.com/qdrant/":           {"fairy/memory"},
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

func TestObsoletePostgresPackageIsAbsent(t *testing.T) {
	if _, err := os.Stat("postgres"); err == nil {
		t.Fatal("obsolete top-level postgres package still exists")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat obsolete postgres package: %v", err)
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
				if strings.Contains(line, `"fairy/internal/`) || strings.Contains(line, `"fairy/companion`) || strings.Contains(line, `"fairy/memory`) {
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

func TestApplicationDoesNotIntroduceDynamicWorkflowRuntime(t *testing.T) {
	forbidden := []string{
		"type WorkflowCatalog ",
		"type WorkflowDefinition ",
		"type WorkflowBlueprint ",
		"type WorkflowDSL ",
		"type NodeCatalog ",
		"type turnGraphProgram ",
		"type RuleTree ",
		"type DesktopTypedGraph ",
		"type DesktopGraphPlan ",
		"type desktopGraphBuilder ",
		"type desktopGraphStep ",
		"type desktopGraphEdge ",
		"type ExpressionEngine ",
		"type ExpressionWorkflow ",
		"type StickerRepository ",
		"type StickerService ",
	}
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
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
			for _, declaration := range forbidden {
				if strings.Contains(line, declaration) {
					t.Errorf("production source %s declares forbidden dynamic workflow mechanism %q", path, strings.TrimSpace(declaration))
				}
			}
		}
		return scanner.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}
