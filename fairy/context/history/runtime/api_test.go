package runtime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPostgresPersistenceHelpersStayPrivate(t *testing.T) {
	t.Parallel()

	forbiddenTypes := map[string]struct{}{
		"Querier":    {},
		"RowQuerier": {},
	}
	forbiddenFunctions := map[string]struct{}{
		"RequireConversation":          {},
		"RequireTurn":                  {},
		"NextTurnRuntimeEventSequence": {},
		"InsertTurnRuntimeEvent":       {},
		"ListTurnRuntimeEvents":        {},
		"SaveLaneContinuation":         {},
		"LoadLaneContinuation":         {},
		"DeleteLaneContinuation":       {},
		"UpsertContextWindow":          {},
		"SaveContextWindow":            {},
		"LoadContextWindow":            {},
	}

	files := parseRuntimeProductionFiles(t)
	for path, file := range files {
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if declaration.Recv != nil {
					continue
				}
				if _, forbidden := forbiddenFunctions[declaration.Name.Name]; forbidden {
					t.Errorf("%s exports PostgreSQL persistence helper %s", path, declaration.Name.Name)
				}
			case *ast.GenDecl:
				for _, specification := range declaration.Specs {
					typeSpec, ok := specification.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if _, forbidden := forbiddenTypes[typeSpec.Name.Name]; forbidden {
						t.Errorf("%s exports PostgreSQL query interface %s", path, typeSpec.Name.Name)
					}
				}
			}
		}
	}
}

func TestExportedAPIDoesNotExposePGX(t *testing.T) {
	t.Parallel()

	foundPoolConstructor := false
	for path, file := range parseRuntimeProductionFiles(t) {
		pgxAliases := runtimePGXImportAliases(t, file)
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if !declaration.Name.IsExported() {
					continue
				}
				if declaration.Recv == nil && declaration.Name.Name == "NewStoreFromPool" {
					foundPoolConstructor = true
				}
				assertRuntimeNodeHasNoPGX(t, path, "function "+declaration.Name.Name, declaration.Type, pgxAliases)
			case *ast.GenDecl:
				for _, specification := range declaration.Specs {
					typeSpec, ok := specification.(*ast.TypeSpec)
					if ok && typeSpec.Name.IsExported() {
						assertRuntimeNodeHasNoPGX(t, path, "type "+typeSpec.Name.Name, typeSpec.Type, pgxAliases)
					}
				}
			}
		}
	}
	if !foundPoolConstructor {
		t.Error("NewStoreFromPool migration constructor is no longer exported")
	}
}

func assertRuntimeNodeHasNoPGX(t *testing.T, path, label string, node ast.Node, aliases map[string]struct{}) {
	t.Helper()
	ast.Inspect(node, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		owner, ok := selector.X.(*ast.Ident)
		if ok {
			if _, leaked := aliases[owner.Name]; leaked {
				t.Errorf("%s exported %s exposes %s.%s", path, label, owner.Name, selector.Sel.Name)
			}
		}
		return true
	})
}

func parseRuntimeProductionFiles(t *testing.T) map[string]*ast.File {
	t.Helper()

	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list runtime Go files: %v", err)
	}
	files := make(map[string]*ast.File)
	fileSet := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files[path] = file
	}
	return files
}

func runtimePGXImportAliases(t *testing.T, file *ast.File) map[string]struct{} {
	t.Helper()

	aliases := make(map[string]struct{})
	for _, specification := range file.Imports {
		path, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			t.Fatalf("decode import %s: %v", specification.Path.Value, err)
		}
		if path != "github.com/jackc/pgx/v5" && path != "github.com/jackc/pgx/v5/pgconn" && path != "github.com/jackc/pgx/v5/pgtype" {
			continue
		}
		alias := filepath.Base(path)
		if specification.Name != nil {
			alias = specification.Name.Name
		}
		if alias == "." {
			t.Fatal("history runtime must not dot-import pgx")
		}
		if alias != "_" {
			aliases[alias] = struct{}{}
		}
	}
	return aliases
}
