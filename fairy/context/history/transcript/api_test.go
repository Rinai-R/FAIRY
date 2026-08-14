package transcript

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPostgresPersistenceAPIStaysPrivate(t *testing.T) {
	t.Parallel()

	forbidden := map[string]struct{}{
		"ConversationDB": {}, "RowQuerier": {}, "Querier": {},
		"RecentConversationID": {}, "InsertConversationWithPromptWindow": {},
		"SelectEndpointConversation": {}, "InsertEndpointConversation": {}, "TouchEndpointConversation": {},
		"LookupEndpointBinding": {}, "LoadConversationBootstrap": {}, "RequireConversation": {},
		"NextSequence": {}, "TouchConversation": {}, "InsertUserTurn": {}, "InsertInitiationTurn": {},
		"CompleteTurn": {}, "InterruptTurn": {}, "InsertAssistantMessage": {}, "FailTurn": {},
		"ScanMessageRecord": {}, "ListConversationMessagesBefore": {},
	}
	foundPoolConstructor := false
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(files, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		pgxAliases := transcriptPGXAliases(t, file)
		if len(pgxAliases) > 0 {
			t.Errorf("%s imports pgx in production code", path)
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if !ast.IsExported(declaration.Name.Name) {
					continue
				}
				if declaration.Recv == nil {
					if _, leaked := forbidden[declaration.Name.Name]; leaked {
						t.Errorf("%s exports PostgreSQL helper %s", path, declaration.Name.Name)
					}
					if declaration.Name.Name == "NewStoreFromPool" {
						foundPoolConstructor = true
					}
				}
				ast.Inspect(declaration.Type, func(node ast.Node) bool {
					selector, ok := node.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					owner, ok := selector.X.(*ast.Ident)
					if ok {
						if _, leaked := pgxAliases[owner.Name]; leaked {
							t.Errorf("%s exported function %s exposes %s.%s", path, declaration.Name.Name, owner.Name, selector.Sel.Name)
						}
					}
					return true
				})
			case *ast.GenDecl:
				for _, specification := range declaration.Specs {
					typeSpec, ok := specification.(*ast.TypeSpec)
					if !ok || !ast.IsExported(typeSpec.Name.Name) {
						continue
					}
					if _, leaked := forbidden[typeSpec.Name.Name]; leaked {
						t.Errorf("%s exports PostgreSQL query type %s", path, typeSpec.Name.Name)
					}
					ast.Inspect(typeSpec.Type, func(node ast.Node) bool {
						selector, ok := node.(*ast.SelectorExpr)
						if !ok {
							return true
						}
						owner, ok := selector.X.(*ast.Ident)
						if ok {
							if _, leaked := pgxAliases[owner.Name]; leaked {
								t.Errorf("%s exported type %s exposes %s.%s", path, typeSpec.Name.Name, owner.Name, selector.Sel.Name)
							}
						}
						return true
					})
				}
			}
		}
	}
	if foundPoolConstructor {
		t.Error("NewStoreFromPool migration constructor must not be exported")
	}
}

func transcriptPGXAliases(t *testing.T, file *ast.File) map[string]struct{} {
	t.Helper()
	aliases := make(map[string]struct{})
	for _, specification := range file.Imports {
		path, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if path != "github.com/jackc/pgx/v5" && path != "github.com/jackc/pgx/v5/pgconn" && path != "github.com/jackc/pgx/v5/pgtype" {
			continue
		}
		alias := filepath.Base(path)
		if specification.Name != nil {
			alias = specification.Name.Name
		}
		if alias == "." {
			t.Fatal("transcript must not dot-import pgx")
		}
		if alias != "_" {
			aliases[alias] = struct{}{}
		}
	}
	return aliases
}
