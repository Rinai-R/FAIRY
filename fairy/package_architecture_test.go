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
	"fairy/context/character",
	"fairy/app/cmd",
	"fairy/app/edge",
	"fairy/app/foundation",
	"fairy/app/session",
	"fairy/runtime/config",
	"fairy/agent/conversation",
	"fairy/agent/conversation/contextplan",
	"fairy/agent/conversation/delivery",
	"fairy/agent/conversation/interaction",
	"fairy/agent/conversation/lifecycle",
	"fairy/agent/conversation/turngate",
	"fairy/app/core",
	"fairy/runtime/seekdb",
	"fairy/runtime/embedding",
	"fairy/runtime/ledger",
	"fairy/transport/desktopcapture",
	"fairy/context/history/compaction",
	"fairy/context/history/expression",
	"fairy/context/history/projection",
	"fairy/context/history/runtime",
	"fairy/context/history/transcript",
	"fairy/context/identity",
	"fairy/context/knowledge",
	"fairy/context/learning/discovery",
	"fairy/agent/learning",
	"fairy/context/memory/admin",
	"fairy/context/memory/extraction",
	"fairy/context/memory/personal",
	"fairy/context/memory/retrieval",
	"fairy/context/recall",
	"fairy/context/social",
	"fairy/runtime/model",
	"fairy/runtime/observability",
	"fairy/runtime/observability/history",
	"fairy/agent/presence",
	"fairy/agent/reply",
	"fairy/transport/session",
	"fairy/agent/sticker",
	"fairy/agent/tool",
	"fairy/transport/web",
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

func TestProductionPackagesLiveUnderRegisteredSubsystems(t *testing.T) {
	allowed := map[string]bool{
		"agent": true, "app": true, "context": true, "runtime": true, "transport": true,
	}
	for _, pkg := range listPackages(t, "./...") {
		if pkg.ImportPath == "fairy" {
			continue
		}
		relative := strings.TrimPrefix(pkg.ImportPath, "fairy/")
		parts := strings.Split(relative, "/")
		if len(parts) < 2 || !allowed[parts[0]] {
			t.Errorf("production package %s is outside a registered subsystem leaf", pkg.ImportPath)
		}
	}
	for namespace := range allowed {
		entries, err := os.ReadDir(namespace)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
				t.Errorf("subsystem namespace %s contains Go source %s; behavior belongs in a leaf package", namespace, entry.Name())
			}
		}
	}
}

func TestFunctionalNamespacesContainNoGoSource(t *testing.T) {
	for _, namespace := range []string{
		filepath.Join("context", "history"),
		filepath.Join("context", "memory"),
	} {
		entries, err := os.ReadDir(namespace)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
				t.Errorf("functional namespace %s contains Go source %s; a child package must own it", namespace, entry.Name())
			}
		}
	}
}

func TestTranscriptDoesNotDependOnCompaction(t *testing.T) {
	for _, pkg := range listPackages(t, "./context/history/transcript") {
		for _, imported := range pkg.Imports {
			if imported == "fairy/context/history/compaction" {
				t.Errorf("transcript owner depends on compaction owner")
			}
		}
	}
}

func TestExtractedStoresDoNotLeakBackIntoMemory(t *testing.T) {
	for _, name := range []string{
		"conversation.go", "runtime_state.go", "compaction.go",
		"store_conversation.go", "store_runtime_state.go", "store_compaction.go",
		"tool_execution.go", "store_usage.go", "usage.go",
		"identity_store.go", "social.go", "social_person.go", "social_types.go",
		"store_social.go", "store_social_api.go", "store_social_person.go", "store_social_person_api.go",
		"store_knowledge.go", "store_knowledge_api.go", "store_knowledge_documents.go", "store_knowledge_ingest.go",
	} {
		if _, err := os.Stat(filepath.Join("context", "memory", name)); err == nil {
			t.Errorf("context/memory regained extracted responsibility %s", name)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat context/memory/%s: %v", name, err)
		}
	}
	for _, required := range []string{
		filepath.Join("context", "history", "transcript", "store_seekdb.go"),
		filepath.Join("context", "history", "compaction", "store_seekdb.go"),
		filepath.Join("context", "history", "runtime", "store_seekdb.go"),
		filepath.Join("runtime", "ledger", "tool_execution_types.go"),
		filepath.Join("runtime", "ledger", "store_usage_api.go"),
		filepath.Join("context", "identity", "store.go"),
		filepath.Join("context", "social", "social_shared.go"),
		filepath.Join("context", "knowledge", "store_seekdb_documents.go"),
	} {
		if _, err := os.Stat(required); err != nil {
			t.Errorf("required responsibility owner %s is missing: %v", required, err)
		}
	}
}

func TestKnowledgeSQLDoesNotLeakIntoMemory(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("context", "memory"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join("context", "memory", entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), "knowledge_entries") || strings.Contains(string(content), "knowledge_sources") {
			t.Errorf("knowledge SQL leaked into %s", path)
		}
	}
}

func TestPersonalMemorySQLDoesNotLeakIntoExtraction(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("context", "memory", "extraction"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join("context", "memory", "extraction", entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), "personal_memories") {
			t.Errorf("personal-memory SQL leaked into %s", path)
		}
	}
}

func TestRuntimePackagesDoNotDependOnContextDomains(t *testing.T) {
	for _, pkg := range listPackages(t, "./runtime/...") {
		for _, imported := range pkg.Imports {
			if strings.HasPrefix(imported, "fairy/context/") {
				t.Errorf("runtime package %s depends on context domain %s", pkg.ImportPath, imported)
			}
		}
	}
}

func TestRemovedTTSSurfacesDoNotReturn(t *testing.T) {
	tokens := []string{
		"fairy/" + "speech",
		"config/" + "speech",
		"speech" + "Enabled",
		"speech" + "Text",
		"Speech" + "Service",
		"PromptLane" + "Translate",
		"createSpeech" + "Playback",
		"audio" + "Unavailable",
	}
	roots := []string{".", "../web/src", "../surfaces/turnclient", "../surfaces/desktop"}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				if info.Name() == "dist" || info.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.Contains(info.Name(), ".test.") || strings.HasSuffix(info.Name(), "_test.go") {
				return nil
			}
			extension := filepath.Ext(path)
			if extension != ".go" && extension != ".js" && extension != ".mjs" && extension != ".jsx" && extension != ".ts" && extension != ".tsx" {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, token := range tokens {
				if strings.Contains(string(content), token) {
					t.Errorf("removed TTS token %q returned in %s", token, path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", root, err)
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
		if slices.Contains([]string{"fairy", "fairy/transport/web", "fairy/app/cmd", "fairy/app/core", "fairy/app/edge"}, pkg.ImportPath) {
			continue
		}
		for _, imported := range pkg.Imports {
			if slices.Contains([]string{"fairy/transport/web", "fairy/app/cmd", "fairy/app/core", "fairy/app/edge"}, imported) {
				t.Errorf("business package %s imports composition package %s", pkg.ImportPath, imported)
			}
		}
	}
}

func TestMemoryPackageDoesNotImportTurn(t *testing.T) {
	for _, pkg := range listPackages(t, "./context/memory/...") {
		for _, imported := range pkg.Imports {
			if imported == "fairy/agent/conversation" {
				t.Errorf("memory owner imports turn consumer package %s", imported)
			}
		}
	}
}

func TestRemovedTechnicalPackagesDoNotReturn(t *testing.T) {
	removed := []string{"companion", "agenttool", "persona", "coreclient", "coredb", "api", "orchestration"}
	for _, directory := range removed {
		if _, err := os.Stat(directory); !os.IsNotExist(err) {
			t.Errorf("removed technical package %s exists again: %v", directory, err)
		}
	}
	for _, pkg := range listPackages(t, "./...") {
		for _, imported := range pkg.Imports {
			for _, directory := range removed {
				path := "fairy/" + directory
				if imported == path || strings.HasPrefix(imported, path+"/") {
					t.Errorf("package %s imports removed package %s", pkg.ImportPath, imported)
				}
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
	for _, directory := range []string{"context/memory", "runtime/seekdb"} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
				strings.HasSuffix(entry.Name(), "_test.go") ||
				directory == "runtime/seekdb" && strings.HasPrefix(entry.Name(), "schema_") {
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
		"context/social/store_seekdb_feedback.go":  true,
		"runtime/seekdb/schema_social_feedback.go": true,
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

func TestExecutionDomainsDoNotImportEachOther(t *testing.T) {
	executionDomains := []string{"fairy/agent/conversation", "fairy/agent/presence", "fairy/agent/learning"}
	for _, pkg := range listPackages(t, "./agent/conversation", "./agent/presence", "./agent/learning") {
		for _, imported := range pkg.Imports {
			if slices.Contains(executionDomains, imported) {
				t.Errorf("execution domain %s imports peer %s; core must adapt lifecycle boundaries", pkg.ImportPath, imported)
			}
		}
	}
}

func TestCapabilityPackagesDoNotImportExecutionOrComposition(t *testing.T) {
	capabilities := []string{
		"./context/knowledge", "./context/memory/...", "./context/social", "./runtime/model", "./context/character", "./agent/sticker", "./transport/desktopcapture", "./agent/reply", "./agent/tool",
	}
	forbidden := []string{
		"fairy/agent/conversation", "fairy/agent/presence", "fairy/agent/learning",
		"fairy/app/core", "fairy/app/edge", "fairy/transport/web", "fairy/app/cmd",
	}
	for _, pkg := range listPackages(t, capabilities...) {
		for _, imported := range pkg.Imports {
			if slices.Contains(forbidden, imported) {
				t.Errorf("capability package %s imports execution/composition package %s", pkg.ImportPath, imported)
			}
		}
	}
}

func TestOnlyCoreComposesExecutionDomains(t *testing.T) {
	executionDomains := []string{"fairy/agent/conversation", "fairy/agent/presence", "fairy/agent/learning"}
	for _, pkg := range listPackages(t, "./...") {
		if pkg.ImportPath == "fairy/app/core" {
			continue
		}
		for _, imported := range pkg.Imports {
			if slices.Contains(executionDomains, imported) {
				t.Errorf("package %s composes execution domain %s outside core", pkg.ImportPath, imported)
			}
		}
	}
}

func TestThirdPartySDKImportsMatchMigrationInventory(t *testing.T) {
	allowed := map[string][]string{
		"github.com/openai/":           {"fairy/runtime/model"},
		"github.com/cloudwego/hertz/":  {"fairy/transport/web"},
		"github.com/gorilla/websocket": {"fairy/transport/web", "fairy/transport/session"},
		"github.com/spf13/cobra":       {"fairy/app/cmd"},
		"github.com/spf13/viper":       {"fairy/app/cmd"},
	}
	forbidden := []string{
		"github.com/jackc/pgx",
		"github.com/pgvector/pgvector-go",
		"gorm.io/",
		"fairy/runtime/database",
	}
	for _, pkg := range listPackages(t, "./...") {
		for _, imported := range pkg.Imports {
			for _, prefix := range forbidden {
				if imported == prefix || strings.HasPrefix(imported, prefix) {
					t.Errorf("package %s still imports removed storage SDK %s", pkg.ImportPath, imported)
				}
			}
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
	if _, err := os.Stat("runtime/database"); err == nil {
		t.Fatal("removed PostgreSQL runtime/database package still exists")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat runtime/database: %v", err)
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
				if strings.Contains(line, `"fairy/`) && !surfaceImportAllowed(surface, line) {
					t.Errorf("independent Surface source %s imports outside the public session boundary: %s", path, strings.TrimSpace(line))
				}
			}
			return scanner.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func surfaceImportAllowed(surface, line string) bool {
	if strings.Contains(line, `"fairy/transport/session"`) {
		return true
	}
	return surface == "desktop" && strings.Contains(line, `"fairy/app/edge"`)
}

func TestQQSurfaceDependencyGraphExcludesCoreDatabase(t *testing.T) {
	command := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{.ImportPath}}", ".")
	command.Dir = filepath.Join("..", "surfaces", "qq-onebot")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list QQ Surface dependencies: %v\n%s", err, output)
	}

	forbidden := []string{
		"fairy/runtime/database",
		"github.com/jackc/pgx",
		"github.com/pgvector/pgvector-go",
		"gorm.io",
	}
	for _, dependency := range strings.Fields(string(output)) {
		for _, prefix := range forbidden {
			if dependency == prefix || strings.HasPrefix(dependency, prefix+"/") {
				t.Errorf("QQ Surface dependency graph contains Core database package %q", dependency)
			}
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
	for _, directory := range []string{"context/memory", "agent/conversation"} {
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
