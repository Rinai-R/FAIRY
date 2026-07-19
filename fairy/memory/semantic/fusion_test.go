package semantic

import (
	"errors"
	"testing"
)

func TestFuseIsDeterministicAndRespectsLimit(t *testing.T) {
	in := []Candidate{
		{ID: "b", Kind: "personal", HasFTS: true, FTSRank: 2, ConfidenceBP: 8000, UpdatedAtMS: 2},
		{ID: "a", Kind: "personal", HasVector: true, VectorSim: 0.9, ConfidenceBP: 9000, UpdatedAtMS: 1},
		{ID: "a", Kind: "personal", HasFTS: true, FTSRank: 1, ConfidenceBP: 9000, UpdatedAtMS: 3},
		{ID: "c", Kind: "knowledge", HasFTS: true, FTSRank: 5, ConfidenceBP: 5000, UpdatedAtMS: 1},
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

func TestUnavailableEmbedder(t *testing.T) {
	var e Embedder = UnavailableEmbedder{}
	if e.Ready() || e.Status() != StatusUnavailable {
		t.Fatalf("embedder = ready=%v status=%s", e.Ready(), e.Status())
	}
	if _, err := e.Embed([]string{"hi"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}
