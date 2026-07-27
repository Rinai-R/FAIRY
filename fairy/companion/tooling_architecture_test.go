package companion

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestToolingPackageDoesNotDependOnFacadeCompositionOrSurfaces(t *testing.T) {
	files, err := filepath.Glob("../tooling/*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, filename := range files {
		if strings.HasSuffix(filename, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filename, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", filename, err)
		}
		for _, importSpec := range file.Imports {
			path := strings.Trim(importSpec.Path.Value, `"`)
			if slices.ContainsFunc([]string{
				"fairy/companion",
				"fairy/runtime",
				"fairy/api",
				"fairy-desktop",
				"fairy-qq-onebot",
				"fairy-surfaces",
			}, func(prefix string) bool {
				return path == prefix || strings.HasPrefix(path, prefix+"/")
			}) {
				t.Errorf("%s imports forbidden upper-layer package %s", filename, path)
			}
		}
	}
}

func TestCompanionToolingCompatibilityFunctionsAreForwarders(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "cognition.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"RespondToolSpecs":                  "ToolSpecs",
		"RespondToolSpecsForInteraction":    "ToolSpecsForInteraction",
		"RespondInstructionsForTools":       "InstructionsForTools",
		"modelDrivenToolBudget":             "ModelDrivenToolBudget",
		"RespondInstructionsForInteraction": "InstructionsForInteraction",
		"parseToolQuery":                    "ParseQuery",
		"mergeRetrievalContext":             "MergeRetrievalContext",
		"retrievalFromWebHits":              "FromWebHits",
		"retrievalFromToolError":            "FromToolError",
	}
	found := make(map[string]bool, len(want))
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		target, tracked := want[function.Name.Name]
		if !tracked {
			continue
		}
		found[function.Name.Name] = true
		if !isSingleToolingForwarder(function, target) {
			t.Errorf("%s must directly forward to tooling.%s", function.Name.Name, target)
		}
	}
	for name := range want {
		if !found[name] {
			t.Errorf("missing Companion compatibility forwarder %s", name)
		}
	}
}

func TestToolingOwnsPureImplementationsAndTurnEngineUsesOwner(t *testing.T) {
	cognition, err := parser.ParseFile(token.NewFileSet(), "cognition.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbiddenFunctions := []string{
		"mergePersonalMemories",
		"mergeKnowledge",
		"mergeSocialMemory",
		"mergeSemanticStatus",
	}
	for _, declaration := range cognition.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			if slices.Contains(forbiddenFunctions, declaration.Name.Name) {
				t.Errorf("Companion duplicates tooling implementation %s", declaration.Name.Name)
			}
		case *ast.GenDecl:
			for _, specification := range declaration.Specs {
				typeSpec, ok := specification.(*ast.TypeSpec)
				if ok && typeSpec.Name.Name == "toolQueryArgs" {
					t.Error("Companion duplicates tooling query contract type")
				}
			}
		}
	}

	turnExecute, err := parser.ParseFile(token.NewFileSet(), "turn_execute.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	requiredSelectors := map[string]bool{
		"ModelDrivenToolBudget":      false,
		"InstructionsForInteraction": false,
		"ParseQuery":                 false,
		"MergeRetrievalContext":      false,
		"FromWebHits":                false,
		"FromToolError":              false,
	}
	ast.Inspect(turnExecute, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		owner, ok := selector.X.(*ast.Ident)
		if ok && owner.Name == "tooling" {
			if _, required := requiredSelectors[selector.Sel.Name]; required {
				requiredSelectors[selector.Sel.Name] = true
			}
		}
		return true
	})
	for selector, found := range requiredSelectors {
		if !found {
			t.Errorf("TurnEngine does not consume tooling.%s", selector)
		}
	}
}

func isSingleToolingForwarder(function *ast.FuncDecl, target string) bool {
	if function.Body == nil || len(function.Body.List) != 1 {
		return false
	}
	returnStatement, ok := function.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(returnStatement.Results) != 1 {
		return false
	}
	call, ok := returnStatement.Results[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != target {
		return false
	}
	owner, ok := selector.X.(*ast.Ident)
	return ok && owner.Name == "tooling"
}
