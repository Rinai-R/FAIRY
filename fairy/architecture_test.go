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
			if (imported == "github.com/spf13/cobra" || imported == "github.com/spf13/viper") && pkg.ImportPath != "fairy/cmd" {
				t.Fatalf("%s imports CLI framework package %s; only fairy/cmd may import Cobra/Viper", pkg.ImportPath, imported)
			}
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

func TestProductionBuildHasNoSQLite(t *testing.T) {
	forbidden := []string{
		"modernc.org/sqlite",
		"sqlite-vec",
		".sqlite3",
		"PRAGMA",
		"fts5",
		"vec0",
	}

	cmd := exec.Command("go", "list", "-deps", "./...")
	dependencies, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./...: %v\n%s", err, dependencies)
	}
	for _, marker := range forbidden[:2] {
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
			for _, marker := range forbidden {
				if strings.Contains(string(source), marker) {
					t.Fatalf("production source %s contains forbidden SQLite marker %q", path, marker)
				}
			}
		}
	}

	binaryPath := filepath.Join(t.TempDir(), "fairy")
	cmd = exec.Command("go", "build", "-o", binaryPath, ".")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read production binary: %v", err)
	}
	for _, marker := range forbidden {
		if strings.Contains(string(binary), marker) {
			t.Fatalf("production binary contains forbidden SQLite marker %q", marker)
		}
	}
}

func TestCompanionLogsDoNotEmitConversationTextFields(t *testing.T) {
	files, err := filepath.Glob("companion/*.go")
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
			if field == "displayText" || field == "speechText" {
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
	files, err := filepath.Glob("cmd/*.go")
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
