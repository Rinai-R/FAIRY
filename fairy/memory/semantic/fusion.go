package semantic

import (
	"math"
	"sort"
)

// Status describes whether semantic ranking participated in a retrieve call.
type Status string

const (
	StatusUnavailable Status = "unavailable"
	StatusReady       Status = "ready"
	StatusUsed        Status = "used"
)

// Candidate is a retrieval hit from FTS and/or vector KNN before fusion.
type Candidate struct {
	ID           string
	Kind         string // personal | knowledge
	FTSRank      float64
	VectorSim    float64 // 0..1 similarity; 0 if absent
	HasFTS       bool
	HasVector    bool
	UpdatedAtMS  int64
	ConfidenceBP uint16
}

// Fuse merges FTS and vector candidates with a fixed weighted formula.
// Higher score is better. Deterministic for identical inputs.
func Fuse(candidates []Candidate, limit int) []Candidate {
	if limit <= 0 {
		return nil
	}
	byID := map[string]Candidate{}
	for _, item := range candidates {
		if item.ID == "" {
			continue
		}
		existing, ok := byID[item.ID]
		if !ok {
			byID[item.ID] = item
			continue
		}
		byID[item.ID] = mergeCandidate(existing, item)
	}
	out := make([]Candidate, 0, len(byID))
	for _, item := range byID {
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := score(out[i]), score(out[j])
		if si != sj {
			return si > sj
		}
		if out[i].UpdatedAtMS != out[j].UpdatedAtMS {
			return out[i].UpdatedAtMS > out[j].UpdatedAtMS
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func mergeCandidate(a, b Candidate) Candidate {
	merged := a
	if b.HasFTS {
		merged.HasFTS = true
		if !a.HasFTS || b.FTSRank < a.FTSRank {
			merged.FTSRank = b.FTSRank
		}
	}
	if b.HasVector {
		merged.HasVector = true
		if b.VectorSim > a.VectorSim {
			merged.VectorSim = b.VectorSim
		}
	}
	if b.UpdatedAtMS > a.UpdatedAtMS {
		merged.UpdatedAtMS = b.UpdatedAtMS
	}
	if b.ConfidenceBP > a.ConfidenceBP {
		merged.ConfidenceBP = b.ConfidenceBP
	}
	if merged.Kind == "" {
		merged.Kind = b.Kind
	}
	return merged
}

// score = 0.55 * fts_norm + 0.35 * vector_sim + 0.10 * confidence_norm
func score(c Candidate) float64 {
	fts := 0.0
	if c.HasFTS {
		// bm25-like ranks are lower-is-better; map into (0,1]
		fts = 1.0 / (1.0 + math.Max(0, c.FTSRank))
	}
	vec := 0.0
	if c.HasVector {
		vec = clamp01(c.VectorSim)
	}
	conf := float64(c.ConfidenceBP) / 10000.0
	return 0.55*fts + 0.35*vec + 0.10*conf
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Embedder generates vectors for memory/knowledge content.
type Embedder interface {
	Ready() bool
	Status() Status
	Embed(texts []string) ([][]float32, error)
	Dims() int
}

// UnavailableEmbedder represents an embedding provider that is not configured.
type UnavailableEmbedder struct{}

func (UnavailableEmbedder) Ready() bool    { return false }
func (UnavailableEmbedder) Status() Status { return StatusUnavailable }
func (UnavailableEmbedder) Dims() int      { return 0 }
func (UnavailableEmbedder) Embed([]string) ([][]float32, error) {
	return nil, ErrUnavailable
}
