package compaction

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestExportedAPIExcludesPostgresPrimitives(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read compaction package: %v", err)
	}
	forbiddenFunctions := map[string]struct{}{
		"CommitCompaction":       {},
		"CommitPromptProjection": {},
		"CommitPromptWindow":     {},
		"CommitTieredCompaction": {},
	}
	files := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(files, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		pgxAliases := postgresImportAliases(t, file)
		if len(pgxAliases) > 0 {
			t.Errorf("%s imports pgx in production code", name)
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if !ast.IsExported(declaration.Name.Name) {
					continue
				}
				if declaration.Recv == nil {
					if _, forbidden := forbiddenFunctions[declaration.Name.Name]; forbidden {
						t.Errorf("%s exports PostgreSQL primitive %s", name, declaration.Name.Name)
					}
				}
				assertCompactionNodeHasNoPGX(t, name, "function "+declaration.Name.Name, declaration.Type, pgxAliases)
			case *ast.GenDecl:
				for _, specification := range declaration.Specs {
					typeSpec, ok := specification.(*ast.TypeSpec)
					if ok && ast.IsExported(typeSpec.Name.Name) {
						assertCompactionNodeHasNoPGX(t, name, "type "+typeSpec.Name.Name, typeSpec.Type, pgxAliases)
					}
				}
			}
		}
	}
}

func assertCompactionNodeHasNoPGX(t *testing.T, path, label string, node ast.Node, aliases map[string]struct{}) {
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

func postgresImportAliases(t *testing.T, file *ast.File) map[string]struct{} {
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
		parts := strings.Split(path, "/")
		alias := parts[len(parts)-1]
		if specification.Name != nil {
			alias = specification.Name.Name
		}
		if alias == "." {
			t.Fatal("compaction must not dot-import pgx")
		}
		if alias != "_" {
			aliases[alias] = struct{}{}
		}
	}
	return aliases
}
