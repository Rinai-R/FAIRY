package main

import (
	"encoding/json"
	"io"
	"os/exec"
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
		}
	}
}
