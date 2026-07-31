package main

import (
	"bufio"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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

func TestProductionMemorySQLDoesNotUseRemovedAuxiliaryTables(t *testing.T) {
	removedTables := []string{
		"feedback_events",
		"knowledge_documents",
		"personal_memory_evidence",
		"social_reply_feedback",
		"social_person_notes",
		"knowledge_sources",
		"knowledge_document_versions",
		"knowledge_chunks",
		"knowledge_evidence",
		"extraction_batches",
		"extraction_batch_turns",
		"knowledge_ingest_jobs",
	}
	for _, directory := range []string{"memory", "coredb"} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
				strings.HasSuffix(entry.Name(), "_test.go") ||
				directory == "coredb" && entry.Name() == "migrate.go" {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lower := strings.ToLower(string(content))
			for _, table := range removedTables {
				for _, prefix := range []string{
					"from " + table,
					"join " + table,
					"into " + table,
					"update " + table,
					"table " + table,
					`return "` + table + `"`,
				} {
					if strings.Contains(lower, prefix) {
						t.Errorf("production source %s still uses removed table %s", path, table)
					}
				}
			}
		}
	}
}

func TestSocialFeedbackLedgerHasNarrowProductionOwner(t *testing.T) {
	allowed := map[string]bool{
		"coredb/migrate.go": true,
		"coredb/schema.go":  true,
		"memory/social.go":  true,
	}
	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		normalized := strings.TrimPrefix(filepath.ToSlash(path), "./")
		if strings.Contains(string(content), "social_memory_feedback_events") && !allowed[normalized] {
			t.Errorf("social feedback ledger is referenced outside its narrow owner: %s", normalized)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
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
		"github.com/pgvector/":         {"fairy/coredb", "fairy/memory"},
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

func TestProductionKnowledgeIngestHasOnlyWholeDocumentActionRuntime(t *testing.T) {
	forbiddenTypes := map[string]struct{}{
		"KnowledgeIngestSnapshot": {},
		"KnowledgeDocumentChunk":  {},
		"KnowledgeIngestFact":     {},
		"KnowledgeIngestRecall":   {},
		"KnowledgeIngestMutation": {},
		"KnowledgeIngestBatch":    {},
	}
	forbiddenFunctions := map[string]struct{}{
		"enqueueKnowledgeIngestSnapshotsPostgres":  {},
		"commitKnowledgeIngestBatchPostgres":       {},
		"processKnowledgeIngestJobsPostgres":       {},
		"commitKnowledgeDocumentMutationsPostgres": {},
	}
	forbiddenStoreMethods := map[string]struct{}{
		"EnqueueKnowledgeIngestSnapshots":          {},
		"EnqueueKnowledgeIngestSnapshotsContext":   {},
		"enqueueKnowledgeIngestSnapshotsPostgres":  {},
		"EnqueueKnowledgeIngestJob":                {},
		"CommitKnowledgeIngestBatch":               {},
		"CommitKnowledgeIngestBatchContext":        {},
		"commitKnowledgeIngestBatchPostgres":       {},
		"RecallKnowledgeForIngest":                 {},
		"RecallKnowledgeForIngestContext":          {},
		"CommitKnowledgeDocumentMutations":         {},
		"CommitKnowledgeDocumentMutationsContext":  {},
		"commitKnowledgeDocumentMutationsPostgres": {},
		"ProcessKnowledgeIngestJobs":               {},
		"ProcessKnowledgeIngestJobsContext":        {},
		"processKnowledgeIngestJobsPostgres":       {},
	}
	forbiddenPreflightIdentifiers := map[string]struct{}{
		"KnowledgeDocumentsNeedExtraction":          {},
		"KnowledgeDocumentsNeedExtractionContext":   {},
		"knowledgeDocumentsNeedExtractionPostgres":  {},
		"validateKnowledgeDocuments":                {},
		"settleKnowledgeDocumentsWithoutExtraction": {},
	}
	forbiddenTaskRuntimeIdentifiers := map[string]struct{}{
		"KnowledgeIngestClaim":                  {},
		"KnowledgeIngestJob":                    {},
		"KnowledgeIngestJobRecord":              {},
		"EnqueueKnowledgeIngestTasks":           {},
		"EnqueueKnowledgeIngestTasksContext":    {},
		"ClaimKnowledgeIngestTasks":             {},
		"ClaimKnowledgeIngestTasksContext":      {},
		"RenewKnowledgeIngestLease":             {},
		"RenewKnowledgeIngestLeaseContext":      {},
		"ReleaseClaimedKnowledgeIngestJob":      {},
		"RetryClaimedKnowledgeIngestJob":        {},
		"FailClaimedKnowledgeIngestJob":         {},
		"DropClaimedKnowledgeIngestJob":         {},
		"EnqueueKnowledgeIngestBatch":           {},
		"EnqueueKnowledgeIngestBatches":         {},
		"EnqueueKnowledgeIngestBatchesContext":  {},
		"enqueueKnowledgeIngestBatchesPostgres": {},
		"ClaimKnowledgeIngestBatches":           {},
		"ClaimKnowledgeIngestBatchesContext":    {},
		"claimKnowledgeIngestBatchesPostgres":   {},
		"ReleaseKnowledgeIngestBatch":           {},
		"ReleaseKnowledgeIngestBatchContext":    {},
		"FailKnowledgeIngestBatch":              {},
		"FailKnowledgeIngestBatchContext":       {},
		"RetryKnowledgeIngestBatch":             {},
		"RetryKnowledgeIngestBatchContext":      {},
		"DropKnowledgeIngestBatch":              {},
		"DropKnowledgeIngestBatchContext":       {},
		"knowledgeIngestBatchFromJob":           {},
		"ClaimLegacyKnowledgeIngestJobs":        {},
		"memoryKnowledgeIngestBatches":          {},
		"persistKnowledgeIngestBatch":           {},
		"validateKnowledgeIngestBatch":          {},
	}
	forbiddenKnowledgeIdentityLiterals := map[string]struct{}{
		"batchId":     {},
		"batchIdHash": {},
		"sourceCount": {},
	}
	var files []string
	for _, directory := range []string{"memory", "companion"} {
		matches, err := filepath.Glob(filepath.Join(directory, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	for _, filename := range files {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filename, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.Ident:
				if _, forbidden := forbiddenPreflightIdentifiers[value.Name]; forbidden {
					t.Errorf("production source %s retains plural knowledge preflight identifier %s", filename, value.Name)
				}
				if _, forbidden := forbiddenTaskRuntimeIdentifiers[value.Name]; forbidden {
					t.Errorf("production source %s retains collection-shaped knowledge task identifier %s", filename, value.Name)
				}
			case *ast.ArrayType:
				if element, ok := value.Elt.(*ast.Ident); ok && element.Name == "KnowledgeDocument" {
					t.Errorf("production source %s retains a KnowledgeDocument collection", filename)
				}
			case *ast.MapType:
				if element, ok := value.Value.(*ast.Ident); ok && element.Name == "KnowledgeDocument" {
					t.Errorf("production source %s retains a map of KnowledgeDocument values", filename)
				}
			}
			return true
		})
		for _, declaration := range file.Decls {
			switch value := declaration.(type) {
			case *ast.GenDecl:
				for _, specification := range value.Specs {
					typeSpec, ok := specification.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if _, forbidden := forbiddenTypes[typeSpec.Name.Name]; forbidden {
						t.Errorf("production source %s retains obsolete knowledge runtime type %s", filename, typeSpec.Name.Name)
					}
					if typeSpec.Name.Name == "KnowledgeIngestTask" || typeSpec.Name.Name == "KnowledgeIngestClaim" {
						structure, ok := typeSpec.Type.(*ast.StructType)
						if ok {
							for _, field := range structure.Fields.List {
								for _, name := range field.Names {
									if name.Name == "Sources" {
										t.Errorf("production source %s retains source collection on %s", filename, typeSpec.Name.Name)
									}
								}
							}
						}
					}
					if typeSpec.Name.Name == "knowledgeAgentPromptPayload" {
						structure, ok := typeSpec.Type.(*ast.StructType)
						if ok {
							for _, field := range structure.Fields.List {
								for _, name := range field.Names {
									if name.Name == "BatchID" {
										t.Errorf("production source %s retains batch identity on Knowledge Agent prompt", filename)
									}
								}
								if field.Tag != nil && strings.Contains(field.Tag.Value, `json:"batchId"`) {
									t.Errorf("production source %s retains batchId JSON field on Knowledge Agent prompt", filename)
								}
							}
						}
					}
					if typeSpec.Name.Name == "KnowledgeIngestJobRecord" {
						structure, ok := typeSpec.Type.(*ast.StructType)
						if ok {
							for _, field := range structure.Fields.List {
								for _, name := range field.Names {
									if name.Name == "BatchID" {
										t.Errorf("production source %s retains batch identity on knowledge job management DTO", filename)
									}
								}
								if field.Tag != nil && strings.Contains(field.Tag.Value, `json:"batchId"`) {
									t.Errorf("production source %s retains batchId JSON field on knowledge job management DTO", filename)
								}
							}
						}
					}
					if typeSpec.Name.Name != "KnowledgeDocument" {
						continue
					}
					structure, ok := typeSpec.Type.(*ast.StructType)
					if !ok {
						continue
					}
					for _, field := range structure.Fields.List {
						for _, name := range field.Names {
							if name.Name == "Chunks" {
								t.Errorf("production source %s retains incoming KnowledgeDocument.Chunks", filename)
							}
						}
					}
				}
			case *ast.FuncDecl:
				if value.Name.Name == "runtimeKnowledgeIngestLedgerMetadata" {
					ast.Inspect(value, func(node ast.Node) bool {
						literal, ok := node.(*ast.BasicLit)
						if !ok || literal.Kind != token.STRING {
							return true
						}
						decoded, err := strconv.Unquote(literal.Value)
						if err != nil {
							return true
						}
						if _, forbidden := forbiddenKnowledgeIdentityLiterals[decoded]; forbidden {
							t.Errorf("production source %s retains %s in knowledge task ledger", filename, decoded)
						}
						return true
					})
				}
				if value.Recv == nil {
					if _, forbidden := forbiddenFunctions[value.Name.Name]; forbidden {
						t.Errorf("production source %s retains obsolete knowledge runtime function %s", filename, value.Name.Name)
					}
					continue
				}
				if _, forbidden := forbiddenStoreMethods[value.Name.Name]; forbidden {
					t.Errorf("production source %s retains obsolete Store method %s", filename, value.Name.Name)
				}
			}
		}
	}
}
