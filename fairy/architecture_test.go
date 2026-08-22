package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNoPackageImportsWails(t *testing.T) {
	cmd := exec.Command("go", "list", "-json", "./...")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list ./...: %v\n%s", err, exitErr.Stderr)
		}
		t.Fatalf("go list ./...: %v", err)
	}

	dec := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var pkg struct {
			ImportPath string
			Imports    []string
		}
		if err := dec.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode go list package: %v", err)
		}
		for _, imported := range pkg.Imports {
			if strings.HasPrefix(imported, "github.com/wailsapp/wails") {
				t.Fatalf("%s imports Wails package %s; Session Core forbids Wails", pkg.ImportPath, imported)
			}
			if imported == "fairy/desktop" {
				t.Fatalf("%s imports removed desktop shell package", pkg.ImportPath)
			}
			if (imported == "github.com/spf13/cobra" || imported == "github.com/spf13/viper") && pkg.ImportPath != "fairy/app/cmd" {
				t.Fatalf("%s imports CLI framework package %s; only fairy/app/cmd may import Cobra/Viper", pkg.ImportPath, imported)
			}
		}
	}
}

func TestDomainPackagesDoNotImportCompositionOrTransport(t *testing.T) {
	domains := []string{
		"./context/character",
		"./runtime/config",
		"./agent/conversation",
		"./runtime/seekdb",
		"./transport/desktopcapture",
		"./context/knowledge",
		"./agent/learning",
		"./context/memory/...",
		"./runtime/model",
		"./runtime/observability",
		"./agent/presence",
		"./transport/session",
		"./agent/sticker",
	}
	forbidden := map[string]struct{}{
		"fairy/app/cmd": {}, "fairy/app/core": {}, "fairy/transport/web": {},
	}

	args := append([]string{"list", "-json"}, domains...)
	out, err := exec.Command("go", args...).Output()
	if err != nil {
		t.Fatalf("go list domain packages: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var pkg struct {
			ImportPath string
			Imports    []string
		}
		if err := decoder.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode domain package: %v", err)
		}
		for _, imported := range pkg.Imports {
			if _, found := forbidden[imported]; found {
				t.Fatalf("domain package %s imports composition/transport package %s", pkg.ImportPath, imported)
			}
		}
	}
}

func TestModelCallSitesProvideExplicitCacheIdentity(t *testing.T) {
	for _, directory := range []string{"agent/conversation", "agent/presence"} {
		files, err := filepath.Glob(filepath.Join(directory, "*.go"))
		if err != nil {
			t.Fatal(err)
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
				case *ast.CallExpr:
					selector, ok := value.Fun.(*ast.SelectorExpr)
					if ok && selector.Sel.Name == "ExecutePrompt" {
						t.Errorf("%s uses legacy ExecutePrompt instead of an explicit CompiledPromptRequest cache identity", filename)
					}
				case *ast.CompositeLit:
					selector, ok := value.Type.(*ast.SelectorExpr)
					if !ok || selector.Sel.Name != "CompiledPromptRequest" {
						return true
					}
					for _, element := range value.Elts {
						field, ok := element.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						name, ok := field.Key.(*ast.Ident)
						if ok && name.Name == "CacheInput" {
							return true
						}
					}
					t.Errorf("%s constructs CompiledPromptRequest without CacheInput", filename)
				}
				return true
			})
		}
	}
}

func TestSessionCoreHasNoDesktopPackage(t *testing.T) {
	if _, err := os.Stat("desktop"); err == nil {
		t.Fatal("fairy/desktop must not exist; desktop shell is not part of Session Core")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat desktop: %v", err)
	}
}

func TestDesktopCaptureHasOneProductionOwner(t *testing.T) {
	if _, err := os.Stat("app/core/capture_hub.go"); err == nil {
		t.Fatal("core must not own the desktop capture state machine")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat core capture hub: %v", err)
	}

	out, err := exec.Command("go", "list", "-json", "./transport/desktopcapture").Output()
	if err != nil {
		t.Fatalf("go list desktopcapture: %v", err)
	}
	var pkg struct {
		Imports []string
	}
	if err := json.Unmarshal(out, &pkg); err != nil {
		t.Fatal(err)
	}
	for _, imported := range pkg.Imports {
		for _, forbidden := range []string{"fairy/app/cmd", "fairy/app/core", "fairy/transport/web"} {
			if imported == forbidden || strings.HasPrefix(imported, forbidden+"/") {
				t.Fatalf("desktopcapture imports forbidden transport/composition package %s", imported)
			}
		}
	}
}

func TestCoreDoesNotRetainTurnEventHistory(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "app/core/runtime.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == "DrainEvents" {
			t.Fatal("core exposes obsolete retained event history through DrainEvents")
		}
		general, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, specification := range general.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != "Runtime" {
				continue
			}
			structure, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, field := range structure.Fields.List {
				slice, ok := field.Type.(*ast.ArrayType)
				if !ok || slice.Len != nil {
					continue
				}
				selector, ok := slice.Elt.(*ast.SelectorExpr)
				owner, ownerOK := selector.X.(*ast.Ident)
				if ok && ownerOK && owner.Name == "turn" && selector.Sel.Name == "TurnEvent" {
					t.Fatal("core retains an unbounded TurnEvent history slice")
				}
			}
		}
	}
}

func TestProductionBuildHasNoSQLite(t *testing.T) {
	sourceForbidden := []string{
		"modernc.org/sqlite",
		"github.com/mattn/go-sqlite3",
		"sqlite-vec",
		".sqlite3",
		"PRAGMA",
		"fts5",
		"vec0",
	}
	binaryForbidden := []string{
		"modernc.org/sqlite",
		"github.com/mattn/go-sqlite3",
		"sqlite-vec",
		"sqlite3_open",
		"sqlite3_prepare_v2",
		"sqlite3_initialize",
		"SQLite format 3",
	}

	cmd := exec.Command("go", "list", "-deps", "./...")
	dependencies, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./...: %v\n%s", err, dependencies)
	}
	for _, marker := range sourceForbidden[:3] {
		if strings.Contains(string(dependencies), marker) {
			t.Fatalf("production dependency graph contains forbidden SQLite marker %q", marker)
		}
	}

	cmd = exec.Command("go", "list", "-json", "./...")
	packagesJSON, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -json ./...: %v", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(packagesJSON)))
	for {
		var pkg struct {
			Dir     string
			GoFiles []string
		}
		if err := decoder.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode go list package: %v", err)
		}
		for _, name := range pkg.GoFiles {
			path := filepath.Join(pkg.Dir, name)
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			for _, marker := range sourceForbidden {
				if strings.Contains(string(source), marker) {
					t.Fatalf("production source %s contains forbidden SQLite marker %q", path, marker)
				}
			}
		}
	}

	binaryPath := filepath.Join(t.TempDir(), "fairy")
	// Raw binary scans must exclude compressed DWARF. A short marker such as
	// "vec0" can otherwise occur by chance in debug data even when neither the
	// dependency graph nor the compiled program contains SQLite.
	cmd = exec.Command("go", "build", "-trimpath", "-ldflags=-w", "-o", binaryPath, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read production binary: %v", err)
	}
	// Do not raw-scan four-byte feature names such as vec0/fts5 here. Even
	// stripped machine code can contain those byte sequences by chance. The
	// source/dependency checks above retain those exact guards; the compiled
	// artifact scan uses linker/library signatures that an actual SQLite
	// runtime cannot avoid.
	for _, marker := range binaryForbidden {
		if strings.Contains(string(binary), marker) {
			t.Fatalf("production binary contains forbidden SQLite marker %q", marker)
		}
	}
}

func TestTurnLogsDoNotEmitConversationTextFields(t *testing.T) {
	files, err := filepath.Glob("conversation/*.go")
	if err != nil {
		t.Fatal(err)
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
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			packageName, ok := selector.X.(*ast.Ident)
			if !ok || packageName.Name != "zap" {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			field, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Errorf("unquote zap field in %s: %v", filename, err)
				return true
			}
			if field == "displayText" {
				t.Errorf("%s emits forbidden conversation text field %q through zap", filename, field)
			}
			return true
		})
	}
}

func TestProductionGraphHasNoGroupEvaluator(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./...")
	dependencies, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./...: %v\n%s", err, dependencies)
	}
	for _, marker := range []string{"fairy/groupeval", "group-eval"} {
		if strings.Contains(string(dependencies), marker) {
			t.Fatalf("production dependency graph contains local evaluator marker %q", marker)
		}
	}
	files, err := filepath.Glob("app/cmd/*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, filename := range files {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		source, err := os.ReadFile(filename)
		if err != nil {
			t.Fatalf("read %s: %v", filename, err)
		}
		for _, marker := range []string{`Use: "eval`, "groupeval", "group-eval"} {
			if strings.Contains(string(source), marker) {
				t.Fatalf("production CLI source %s contains local evaluator marker %q", filename, marker)
			}
		}
	}
}

func TestDesktopCapturePersistenceBoundary(t *testing.T) {
	for _, directory := range []string{"context/memory", "postgres", "runtime/observability"} {
		files, err := filepath.Glob(filepath.Join(directory, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, filename := range files {
			if strings.HasSuffix(filename, "_test.go") {
				continue
			}
			source, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range []string{"ImageDataURL", "DataURL", "data:image/"} {
				if strings.Contains(string(source), marker) {
					t.Fatalf("persistence/observability source %s contains raw capture marker %q", filename, marker)
				}
			}
		}
	}
	schema, err := os.ReadFile("runtime/seekdb/schema_ledger.go")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(schema))
	if !strings.Contains(lower, "create table if not exists tool_executions") {
		t.Fatal("tool_executions schema is missing")
	}
	for _, marker := range []string{"[]byte", "dataurl", "payload", "content"} {
		if strings.Contains(lower, marker) {
			t.Fatalf("tool_executions persists forbidden raw evidence column marker %q", marker)
		}
	}
}

func TestSeekDBRuntimeDoesNotImportProcessOrMySQLDriver(t *testing.T) {
	cmd := exec.Command("go", "list", "-json", "./runtime/seekdb")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list ./runtime/seekdb: %v", err)
	}
	var pkg struct {
		Imports []string
		Deps    []string
	}
	if err := json.Unmarshal(out, &pkg); err != nil {
		t.Fatal(err)
	}
	for _, imported := range pkg.Imports {
		if imported == "os/exec" {
			t.Fatal("runtime/seekdb imports os/exec; in-process SeekDB must not spawn a child process")
		}
		if imported == "github.com/go-sql-driver/mysql" {
			t.Fatal("runtime/seekdb imports go-sql-driver/mysql; in-process SeekDB must not use SQL TCP")
		}
	}
}

func TestEndpointProductionDoesNotSpawnHelpersOrLoadUnregisteredLibraries(t *testing.T) {
	productionRoots := []string{
		"app/core",
		"app/edge",
		"runtime/seekdb",
		"runtime/model",
		filepath.Join("..", "desktop"),
	}
	allowedDynamicLoader := filepath.Clean(filepath.Join("runtime", "seekdb", "embed_api.c"))
	dynamicLoaderMarkers := []string{
		"dlopen(",
		"dlsym(",
		"LoadLibraryA(",
		"LoadLibraryW(",
		"LoadLibraryExA(",
		"LoadLibraryExW(",
		"syscall.LoadDLL(",
		"windows.NewLazyDLL(",
		"purego.Dlopen(",
	}

	for _, root := range productionRoots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info.IsDir() {
				if path != root && (info.Name() == "testdata" || info.Name() == "vendor") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			extension := strings.ToLower(filepath.Ext(path))
			switch extension {
			case ".go", ".c", ".cc", ".cpp", ".m", ".mm":
			default:
				return nil
			}

			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if extension == ".go" {
				file, err := parser.ParseFile(token.NewFileSet(), path, source, parser.ImportsOnly)
				if err != nil {
					return err
				}
				for _, imported := range file.Imports {
					importPath, err := strconv.Unquote(imported.Path.Value)
					if err != nil {
						return err
					}
					if importPath == "os/exec" {
						t.Fatalf("endpoint production source %s imports os/exec; helper subprocesses are forbidden", path)
					}
				}
			}

			for _, marker := range dynamicLoaderMarkers {
				if !strings.Contains(string(source), marker) {
					continue
				}
				cleanPath := filepath.Clean(path)
				if cleanPath != allowedDynamicLoader {
					t.Fatalf("endpoint production source %s uses unregistered dynamic-loader marker %q", path, marker)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan endpoint production root %s: %v", root, err)
		}
	}
}

func TestEndpointStrictCompositionHasOnlyDeclaredNetworkAuthorities(t *testing.T) {
	contracts := []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{
			path: "app/core/semantic.go",
			required: []string{
				"profile == ProfileEndpointStrict",
				"model.NewEndpointModelService",
			},
			forbidden: []string{"http.DefaultClient", "http.ProxyFromEnvironment", "os.Getenv", "exec.Command"},
		},
		{
			path: "app/edge/runtime_openserp.go",
			required: []string{
				"config.ReadWebSearchSettings",
				"config.ResolveEndpointOpenSERPOrigin",
				"openserp.NewAuthority",
			},
			forbidden: []string{"http.DefaultClient", "http.ProxyFromEnvironment", "os.Getenv", "bindWebPlugin", "bindQQPlugin"},
		},
		{
			path: "runtime/model/provider_http.go",
			required: []string{
				"Proxy:                 nil",
				"DialContext:           pinned.DialContext",
				"CheckRedirect:",
			},
			forbidden: []string{"http.DefaultClient", "http.DefaultTransport", "http.ProxyFromEnvironment", "os.Getenv", "exec.Command"},
		},
		{
			path: "transport/openserp/authority.go",
			required: []string{
				"Proxy:                 nil",
				"DialContext:           pinned.DialContext",
				"CheckRedirect:",
			},
			forbidden: []string{"http.DefaultClient", "http.DefaultTransport", "http.ProxyFromEnvironment", "os.Getenv", "exec.Command"},
		},
	}

	for _, contract := range contracts {
		source, err := os.ReadFile(contract.path)
		if err != nil {
			t.Fatalf("read endpoint-strict contract %s: %v", contract.path, err)
		}
		text := string(source)
		for _, marker := range contract.required {
			if !strings.Contains(text, marker) {
				t.Errorf("endpoint-strict contract %s is missing authority marker %q", contract.path, marker)
			}
		}
		for _, marker := range contract.forbidden {
			if strings.Contains(text, marker) {
				t.Errorf("endpoint-strict contract %s contains forbidden network/fallback marker %q", contract.path, marker)
			}
		}
	}

	edgeSource, err := os.ReadFile("app/edge/runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	edgeText := string(edgeSource)
	for _, required := range []string{
		"func OpenEndpointStrict(",
		"options.Profile = ProfileEndpointStrict",
		"runtime.bindOpenSERP()",
	} {
		if !strings.Contains(edgeText, required) {
			t.Fatalf("endpoint-strict Edge composition is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"plugin.NewStore",
		"bindWebPlugin",
		"bindQQPlugin",
	} {
		if strings.Contains(edgeText, forbidden) {
			t.Fatalf("endpoint-strict Edge composition references a forbidden extension: %q", forbidden)
		}
	}
}

func TestDesktopSurfaceDependencyBoundary(t *testing.T) {
	cmd := exec.Command("go", "list", "-json", ".")
	cmd.Dir = filepath.Join("..", "desktop")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go list desktop: %v\n%s", err, exitErr.Stderr)
		}
		t.Fatal(err)
	}
	var pkg struct{ Imports []string }
	if err := json.Unmarshal(out, &pkg); err != nil {
		t.Fatal(err)
	}
	for _, imported := range pkg.Imports {
		for _, forbidden := range []string{"fairy/agent/conversation", "fairy/agent/presence", "fairy/agent/learning", "fairy/app/core", "fairy/context/memory", "fairy/context/knowledge", "fairy/runtime/model", "fairy/transport/web", "github.com/wailsapp/wails/v2"} {
			if imported == forbidden || strings.HasPrefix(imported, forbidden+"/") {
				t.Fatalf("desktop Surface imports forbidden package %s", imported)
			}
		}
	}
}
