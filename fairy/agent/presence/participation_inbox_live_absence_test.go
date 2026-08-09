//go:build !live

package presence

import (
	"encoding/json"
	"os/exec"
	"slices"
	"testing"
)

func TestDefaultBuildExcludesLiveEvalInboxBridge(t *testing.T) {
	command := exec.Command("go", "list", "-json", ".")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var listed struct {
		GoFiles        []string
		IgnoredGoFiles []string
	}
	if err := json.Unmarshal(output, &listed); err != nil {
		t.Fatalf("decode go list: %v", err)
	}
	const bridge = "participation_inbox_live.go"
	if slices.Contains(listed.GoFiles, bridge) {
		t.Fatalf("default build includes %s", bridge)
	}
	if !slices.Contains(listed.IgnoredGoFiles, bridge) {
		t.Fatalf("go list did not identify %s as build-tag excluded", bridge)
	}
}
