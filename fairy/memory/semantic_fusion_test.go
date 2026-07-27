package memory

import (
	"errors"
	"testing"
)

func TestFuseIsDeterministicAndRespectsLimit(t *testing.T) {
	in := []SemanticCandidate{
		{ID: "b", Kind: "personal", HasFTS: true, FTSRank: 2, ConfidenceBP: 8000, UpdatedAtMS: 2},
		{ID: "a", Kind: "personal", HasVector: true, VectorSim: 0.9, ConfidenceBP: 9000, UpdatedAtMS: 1},
		{ID: "a", Kind: "personal", HasFTS: true, FTSRank: 1, ConfidenceBP: 9000, UpdatedAtMS: 3},
		{ID: "c", Kind: "knowledge", HasFTS: true, FTSRank: 5, ConfidenceBP: 5000, UpdatedAtMS: 1},
	}
	first := FuseSemanticCandidates(in, 2)
	second := FuseSemanticCandidates(in, 2)
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

func TestUnavailableEmbedder(t *testing.T) {
	var e SemanticEmbedder = UnavailableSemanticEmbedder{}
	if e.Ready() || e.Status() != SemanticStatusUnavailable {
		t.Fatalf("embedder = ready=%v status=%s", e.Ready(), e.Status())
	}
	if _, err := e.Embed([]string{"hi"}); !errors.Is(err, ErrSemanticUnavailable) {
		t.Fatalf("err = %v", err)
	}
}
