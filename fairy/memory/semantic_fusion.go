package memory

import (
	"sort"
)

// SemanticStatus describes whether semantic ranking participated in a retrieve call.
type SemanticStatus string

const (
	SemanticStatusUnavailable SemanticStatus = "unavailable"
	SemanticStatusReady       SemanticStatus = "ready"
	SemanticStatusUsed        SemanticStatus = "used"
)

// SemanticCandidate is a retrieval hit from FTS and/or vector KNN before fusion.
type SemanticCandidate struct {
	ID           string
	Kind         string // personal | knowledge
	TextScore    float64
	VectorSim    float64 // 0..1 similarity; 0 if absent
	HasText      bool
	HasVector    bool
	UpdatedAtMS  int64
	ConfidenceBP uint16
}

// FuseSemanticCandidates merges FTS and vector candidates with a fixed weighted formula.
// Higher score is better. Deterministic for identical inputs.
func FuseSemanticCandidates(candidates []SemanticCandidate, limit int) []SemanticCandidate {
	if limit <= 0 {
		return nil
	}
	byID := map[string]SemanticCandidate{}
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
	out := make([]SemanticCandidate, 0, len(byID))
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

func mergeCandidate(a, b SemanticCandidate) SemanticCandidate {
	merged := a
	if b.HasText {
		merged.HasText = true
		if !a.HasText || b.TextScore > a.TextScore {
			merged.TextScore = b.TextScore
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
func score(c SemanticCandidate) float64 {
	text := 0.0
	if c.HasText {
		text = clamp01(c.TextScore)
	}
	vec := 0.0
	if c.HasVector {
		vec = clamp01(c.VectorSim)
	}
	conf := float64(c.ConfidenceBP) / 10000.0
	return 0.55*text + 0.35*vec + 0.10*conf
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

// SemanticEmbedder generates vectors for memory/knowledge content.
type SemanticEmbedder interface {
	Ready() bool
	Status() SemanticStatus
	Embed(texts []string) ([][]float32, error)
	Dims() int
}

// UnavailableSemanticEmbedder represents an embedding provider that is not configured.
type UnavailableSemanticEmbedder struct{}

func (UnavailableSemanticEmbedder) Ready() bool            { return false }
func (UnavailableSemanticEmbedder) Status() SemanticStatus { return SemanticStatusUnavailable }
func (UnavailableSemanticEmbedder) Dims() int              { return 0 }
func (UnavailableSemanticEmbedder) Embed([]string) ([][]float32, error) {
	return nil, ErrSemanticUnavailable
}
