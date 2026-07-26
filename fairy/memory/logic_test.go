package memory

import (
	"strings"
	"testing"
)

func TestBuildFTSQueryUsesTrigramsAndRejectsShortRuns(t *testing.T) {
	query, err := BuildFTSQuery("太甜的饮料推荐")
	if err != nil {
		t.Fatalf("buildFTSQuery() error = %v", err)
	}
	for _, part := range []string{`"太甜的"`, `"甜的饮"`, `"的饮料"`, `"饮料推"`, `"料推荐"`} {
		if !strings.Contains(query, part) {
			t.Fatalf("query = %q, missing %s", query, part)
		}
	}
	empty, err := BuildFTSQuery("饮料")
	if err != nil {
		t.Fatalf("short buildFTSQuery() error = %v", err)
	}
	if empty != "" {
		t.Fatalf("short query = %q, want empty", empty)
	}
}

func TestSemanticContentHashIsDeterministicAndContentSensitive(t *testing.T) {
	first := semanticContentHash("topic\nstatement")
	if len(first) != 64 || first != semanticContentHash("topic\nstatement") {
		t.Fatalf("semanticContentHash() = %q, want stable SHA-256 hex", first)
	}
	if first == semanticContentHash("topic\nchanged") {
		t.Fatal("semanticContentHash() did not change with content")
	}
}
