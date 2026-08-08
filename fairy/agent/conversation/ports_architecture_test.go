package conversation

import (
	"fairy/transport/session"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var memoryStoreInterfaceNames = []string{
	"InteractionBindingStore",
	"ConversationActivityStore",
	"ConversationMetadataStore",
	"PromptContextStore",
	"TurnStore",
	"MemoryRetrievalStore",
	"PortraitStore",
	"SocialRetrievalStore",
	"RuntimeStateStore",
	"ContextRetentionStore",
	"SocialContextStore",
	"SocialLearningStore",
}

var retentionPortInterfaceNames = []string{"extractionStore", "knowledgeIngestStore"}

func TestMemoryPortsAreConsumerScoped(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "ports.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := make(map[string]bool, len(memoryStoreInterfaceNames))
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, specification := range general.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if typeSpec.Name.Name == "MemoryPort" || typeSpec.Name.Name == "ConversationStore" {
				t.Errorf("companion still declares obsolete storage interface %s", typeSpec.Name.Name)
			}
			if !slices.Contains(memoryStoreInterfaceNames, typeSpec.Name.Name) {
				continue
			}
			found[typeSpec.Name.Name] = true
			iface, ok := typeSpec.Type.(*ast.InterfaceType)
			if !ok {
				t.Errorf("%s is not an interface", typeSpec.Name.Name)
				continue
			}
			methods := 0
			for _, field := range iface.Methods.List {
				if len(field.Names) == 0 {
					t.Errorf("%s embeds another interface; storage ports must expose their bounded method set directly", typeSpec.Name.Name)
					continue
				}
				methods += len(field.Names)
			}
			if methods == 0 || methods > 8 {
				t.Errorf("%s declares %d methods, want 1..8", typeSpec.Name.Name, methods)
			}
		}
	}
	for _, name := range memoryStoreInterfaceNames {
		if !found[name] {
			t.Errorf("missing consumer-scoped memory interface %s", name)
		}
	}
}

func TestRetentionPortsAreConsumerScoped(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "ports.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := make(map[string]bool, len(retentionPortInterfaceNames))
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, specification := range general.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if !ok || !slices.Contains(retentionPortInterfaceNames, typeSpec.Name.Name) {
				continue
			}
			found[typeSpec.Name.Name] = true
			iface, ok := typeSpec.Type.(*ast.InterfaceType)
			if !ok {
				t.Errorf("%s is not an interface", typeSpec.Name.Name)
				continue
			}
			methods := 0
			for _, field := range iface.Methods.List {
				if len(field.Names) == 0 {
					t.Errorf("%s embeds another interface", typeSpec.Name.Name)
					continue
				}
				methods += len(field.Names)
			}
			if methods == 0 || methods > 8 {
				t.Errorf("%s declares %d methods, want 1..8", typeSpec.Name.Name, methods)
			}
		}
	}
	for _, name := range retentionPortInterfaceNames {
		if !found[name] {
			t.Errorf("missing consumer-scoped retention interface %s", name)
		}
	}
}

func TestCompanionFacadeDoesNotOwnLifecycleState(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "service.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, specification := range general.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != "Service" {
				continue
			}
			structure, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				t.Fatal("Service is not a struct")
			}
			for _, field := range structure.Fields.List {
				if len(field.Names) == 0 {
					continue
				}
				name := field.Names[0].Name
				if slices.Contains([]string{"gateMu", "gates", "activeTurn", "compacting", "extractionMu", "extractionIdle", "backgroundJobs"}, name) {
					t.Errorf("Service still owns lifecycle state field %s", name)
				}
			}
		}
	}
}

func TestLifecycleImplementationDoesNotDependOnComposition(t *testing.T) {
	for _, filename := range []string{"turngate/registry.go", "lifecycle/lifecycle.go", "../learning/service.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, importSpec := range file.Imports {
			path := strings.Trim(importSpec.Path.Value, "\"")
			if strings.HasPrefix(path, "fairy/app/core") || strings.HasPrefix(path, "fairy/transport/web") || strings.HasPrefix(path, "fairy/app/cmd") {
				t.Errorf("%s imports forbidden upper-layer package %s", filename, path)
			}
		}
	}
}

func TestMemoryTestDoublesDoNotEmbedStorageInterfaces(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := append([]string{"MemoryPort"}, memoryStoreInterfaceNames...)
	for _, filename := range files {
		file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filename, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			structure, ok := node.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range structure.Fields.List {
				if len(field.Names) != 0 {
					continue
				}
				ident, ok := field.Type.(*ast.Ident)
				if ok && slices.Contains(forbidden, ident.Name) {
					t.Errorf("%s embeds storage interface %s; implement only the tested consumer methods", filename, ident.Name)
				}
			}
			return true
		})
	}
}

func TestProductionCompanionDoesNotUseUnboundedLoaders(t *testing.T) {
	files, err := filepath.Glob("*.go")
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
			selector, ok := node.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == "LoadConversation" {
				t.Errorf("%s uses full conversation loader in Companion production code", filename)
			}
			if ok && selector.Sel.Name == "List" {
				t.Errorf("%s enumerates a catalog in Companion production code", filename)
			}
			return true
		})
	}
}

func TestCharacterLookupIsIDAddressed(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "ports.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, specification := range general.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if typeSpec.Name.Name == "CharacterCatalog" {
				t.Error("companion still declares obsolete CharacterCatalog")
			}
			if typeSpec.Name.Name != "CharacterLookup" {
				continue
			}
			found = true
			iface, ok := typeSpec.Type.(*ast.InterfaceType)
			if !ok || len(iface.Methods.List) != 1 || len(iface.Methods.List[0].Names) != 1 || iface.Methods.List[0].Names[0].Name != "Lookup" {
				t.Errorf("CharacterLookup must declare only Lookup, got %#v", typeSpec.Type)
			}
		}
	}
	if !found {
		t.Fatal("missing CharacterLookup consumer interface")
	}
}

type companionFacadeContract interface {
	SubmitTurn(SubmitTurnRequest) (TurnOutcome, error)
	SubmitCompiledTurn(SubmitCompiledTurnRequest) (TurnOutcome, error)
	SubmitDesktopInitiation(DesktopInitiationRequest, session.DesktopObservation) (TurnOutcome, error)
	SubmitDesktopVisionInitiation(DesktopVisionInitiationRequest) (TurnOutcome, error)
	CancelTurn(string, string) error
}

var _ companionFacadeContract = (*Service)(nil)
