package retrieval

import (
	"testing"
)

func TestFuseIsDeterministicAndRespectsLimit(t *testing.T) {
	in := []Candidate{
		{ID: "b", Kind: "personal", HasText: true, TextScore: 0.3, ConfidenceBP: 8000, UpdatedAtMS: 2},
		{ID: "a", Kind: "personal", HasVector: true, VectorSim: 0.9, ConfidenceBP: 9000, UpdatedAtMS: 1},
		{ID: "a", Kind: "personal", HasText: true, TextScore: 0.8, ConfidenceBP: 9000, UpdatedAtMS: 3},
		{ID: "c", Kind: "knowledge", HasText: true, TextScore: 0.1, ConfidenceBP: 5000, UpdatedAtMS: 1},
	}
	first := Fuse(in, 2)
	second := Fuse(in, 2)
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("len = %d,%d", len(first), len(second))
	}
	if first[0].ID != second[0].ID || first[1].ID != second[1].ID {
		t.Fatalf("non-deterministic: %#v vs %#v", first, second)
	}
	if first[0].ID != "a" {
		t.Fatalf("expected fused id a first, got %#v", first)
	}
}
