package personal

import (
	memoryretrieval "fairy/context/memory/retrieval"
	"fairy/runtime/embedding"
)

const (
	MaxResultsPerKind        = 4
	MaxRetrievedContextChars = MaxContentRunes
)

type vectorPersonalTruth struct {
	record Retrieved
	score  float64
}

func fusePostgresRetrieval(text Retrieval, vectorMatches map[string]vectorPersonalTruth) Retrieval {
	personalRecords := make(map[string]Retrieved, len(text.PersonalMemories)+len(vectorMatches))
	personalCandidates := make([]memoryretrieval.Candidate, 0, len(text.PersonalMemories)+len(vectorMatches))
	for _, record := range text.PersonalMemories {
		personalRecords[record.ID] = record
		personalCandidates = append(personalCandidates, memoryretrieval.Candidate{
			ID: record.ID, Kind: record.Kind, TextScore: record.TextScore, HasText: true,
			UpdatedAtMS: record.UpdatedAtUnixMS, ConfidenceBP: record.ConfidenceBasisPoints,
		})
	}
	for id, truth := range vectorMatches {
		personalRecords[id] = truth.record
		personalCandidates = append(personalCandidates, memoryretrieval.Candidate{
			ID: id, Kind: truth.record.Kind, VectorSim: truth.score, HasVector: true,
			UpdatedAtMS: truth.record.UpdatedAtUnixMS, ConfidenceBP: truth.record.ConfidenceBasisPoints,
		})
	}
	remaining := MaxRetrievedContextChars
	result := Retrieval{SemanticStatus: string(embedding.SemanticStatusUsed)}
	perKind := make(map[string]int)
	for _, candidate := range memoryretrieval.Fuse(personalCandidates, 64) {
		record, ok := personalRecords[candidate.ID]
		if !ok || perKind[record.Kind] >= MaxResultsPerKind {
			continue
		}
		length := len([]rune(record.Content))
		if length > remaining {
			continue
		}
		remaining -= length
		perKind[record.Kind]++
		result.PersonalMemories = append(result.PersonalMemories, record)
	}
	if len(vectorMatches) == 0 {
		result.SemanticStatus = string(embedding.SemanticStatusReady)
	}
	return result
}
