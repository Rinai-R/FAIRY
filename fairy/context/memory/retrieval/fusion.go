package retrieval

import "sort"

type Candidate struct {
	ID           string
	Kind         string
	TextScore    float64
	VectorSim    float64
	HasText      bool
	HasVector    bool
	UpdatedAtMS  int64
	ConfidenceBP uint16
}

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
		byID[item.ID] = merge(existing, item)
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

func merge(a, b Candidate) Candidate {
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

func score(candidate Candidate) float64 {
	text := 0.0
	if candidate.HasText {
		text = clamp01(candidate.TextScore)
	}
	vector := 0.0
	if candidate.HasVector {
		vector = clamp01(candidate.VectorSim)
	}
	confidence := float64(candidate.ConfidenceBP) / 10000
	return 0.55*text + 0.35*vector + 0.10*confidence
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
