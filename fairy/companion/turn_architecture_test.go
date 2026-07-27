package companion

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestTurnEngineResponsibilitiesStayInCompanionFiles(t *testing.T) {
	expected := map[string][]string{
		"turn_engine.go":   nil,
		"turn_submit.go":   {"SubmitDesktopVisionInitiation", "SubmitDesktopInitiation", "SubmitTurn"},
		"turn_execute.go":  {"SubmitCompiledTurn"},
		"turn_delivery.go": {"declareRespondingNode"},
		"turn_terminal.go": {"declarePersistNode", "fail", "transition"},
		"turn_cancel.go":   {"CancelTurn", "mapModelCancelError"},
	}
	for filename, required := range expected {
		file, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filename, err)
		}
		if file.Name.Name != "companion" {
			t.Errorf("%s package = %s, want companion", filename, file.Name.Name)
		}
		found := make([]string, 0)
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok {
				found = append(found, function.Name.Name)
			}
		}
		for _, name := range required {
			if !slices.Contains(found, name) {
				t.Errorf("%s is missing %s", filename, name)
			}
		}
	}
}

func TestTurnExecutionFileRemainsBounded(t *testing.T) {
	file, err := os.Open("turn_execute.go")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lines := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if lines > 800 {
		t.Fatalf("turn_execute.go has %d lines, want at most 800; move a cohesive lifecycle responsibility instead of growing the execution file", lines)
	}
}

func TestTurnLifecycleImplementationLivesInTurnPackage(t *testing.T) {
	turnFile, err := parser.ParseFile(token.NewFileSet(), "../turn/lifecycle.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	owned := map[string]bool{}
	for _, declaration := range turnFile.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok.String() != "type" {
			continue
		}
		for _, specification := range general.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if ok {
				owned[typeSpec.Name.Name] = true
			}
		}
	}
	for _, name := range []string{"TurnState", "TurnEvent", "TurnLifecycle"} {
		if !owned[name] {
			t.Errorf("turn package does not own %s", name)
		}
	}

	aliasFile, err := parser.ParseFile(token.NewFileSet(), "lifecycle_aliases.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range aliasFile.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok.String() != "type" {
			continue
		}
		for _, specification := range general.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if !ok || !owned[typeSpec.Name.Name] {
				continue
			}
			if typeSpec.Assign == 0 {
				t.Errorf("Companion redeclares turn lifecycle type %s instead of aliasing it", typeSpec.Name.Name)
			}
		}
	}
}

func TestCompanionDoesNotAccessTurnLifecycleMutex(t *testing.T) {
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
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "mu" {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if ok && ident.Name == "life" {
				t.Errorf("%s accesses TurnLifecycle internal mutex directly", filename)
			}
			return true
		})
	}
}
