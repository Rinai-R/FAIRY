package companion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"fairy/memory"
	"fairy/model"
)

const (
	maxKnowledgeIngestFacts          = 8
	maxKnowledgeIngestSubjectRunes   = 300
	maxKnowledgeIngestPredicateRunes = 160
	maxKnowledgeIngestValueRunes     = 600
	maxKnowledgeIngestStatementRunes = 1200
)

type knowledgeIngestFact struct {
	Subject               string   `json:"subject"`
	Predicate             string   `json:"predicate"`
	Value                 string   `json:"value"`
	Statement             string   `json:"statement"`
	ConfidenceBasisPoints uint16   `json:"confidenceBasisPoints"`
	EvidenceChunkIDs      []string `json:"evidenceChunkIDs"`
}

type knowledgeIngestOutput struct {
	Facts []knowledgeIngestFact `json:"facts"`
}

type knowledgeIngestPromptChunk struct {
	ID      string `json:"id"`
	Ordinal int    `json:"ordinal"`
	Text    string `json:"text"`
}

type knowledgeIngestPromptDocument struct {
	SourceID     string                       `json:"sourceId"`
	CanonicalURL string                       `json:"canonicalUrl"`
	Title        string                       `json:"title"`
	Chunks       []knowledgeIngestPromptChunk `json:"chunks"`
}

type knowledgeIngestPromptBatch struct {
	BatchID   string                          `json:"batchId"`
	Documents []knowledgeIngestPromptDocument `json:"documents"`
}

func buildKnowledgeIngestInput(batch memory.KnowledgeIngestBatch, documents []memory.KnowledgeDocument) ([]model.PromptItem, error) {
	if batch.ID == "" || len(documents) == 0 || len(documents) > memory.MaxKnowledgeIngestSources {
		return nil, errors.New("knowledge ingest batch is invalid")
	}
	promptDocuments := make([]knowledgeIngestPromptDocument, 0, len(documents))
	for _, document := range documents {
		chunks := make([]knowledgeIngestPromptChunk, 0, len(document.Chunks))
		for _, chunk := range document.Chunks {
			chunks = append(chunks, knowledgeIngestPromptChunk{ID: chunk.ID, Ordinal: chunk.Ordinal, Text: chunk.Text})
		}
		promptDocuments = append(promptDocuments, knowledgeIngestPromptDocument{
			SourceID: document.SourceID, CanonicalURL: document.CanonicalURL,
			Title: document.Title, Chunks: chunks,
		})
	}
	payload, err := json.Marshal(struct {
		FairyContextData knowledgeIngestPromptBatch `json:"fairy_context_data"`
	}{
		FairyContextData: knowledgeIngestPromptBatch{
			BatchID: batch.ID, Documents: promptDocuments,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("serializing knowledge ingest batch: %w", err)
	}
	return []model.PromptItem{{Type: model.PromptItemContextData, Content: string(payload)}}, nil
}

func parseKnowledgeIngestOutput(raw string, documents []memory.KnowledgeDocument) (knowledgeIngestOutput, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return knowledgeIngestOutput{}, errors.New("knowledge ingest model returned empty output")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	decoder.DisallowUnknownFields()
	var output knowledgeIngestOutput
	if err := decoder.Decode(&output); err != nil {
		return knowledgeIngestOutput{}, errors.New("knowledge ingest model did not return strict JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return knowledgeIngestOutput{}, errors.New("knowledge ingest model returned trailing content")
	}
	if output.Facts == nil {
		return knowledgeIngestOutput{}, errors.New("knowledge ingest facts are required")
	}
	if len(output.Facts) > maxKnowledgeIngestFacts {
		return knowledgeIngestOutput{}, errors.New("knowledge ingest fact limit exceeded")
	}
	allowedChunks := make(map[string]struct{})
	for _, document := range documents {
		for _, chunk := range document.Chunks {
			allowedChunks[chunk.ID] = struct{}{}
		}
	}
	for index := range output.Facts {
		fact := &output.Facts[index]
		if strings.TrimSpace(fact.Subject) != fact.Subject || fact.Subject == "" || utf8.RuneCountInString(fact.Subject) > maxKnowledgeIngestSubjectRunes {
			return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] subject is invalid", index)
		}
		if strings.TrimSpace(fact.Predicate) != fact.Predicate || fact.Predicate == "" || utf8.RuneCountInString(fact.Predicate) > maxKnowledgeIngestPredicateRunes {
			return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] predicate is invalid", index)
		}
		if strings.TrimSpace(fact.Value) != fact.Value || fact.Value == "" || utf8.RuneCountInString(fact.Value) > maxKnowledgeIngestValueRunes {
			return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] value is invalid", index)
		}
		if strings.TrimSpace(fact.Statement) != fact.Statement || utf8.RuneCountInString(fact.Statement) < 8 || utf8.RuneCountInString(fact.Statement) > maxKnowledgeIngestStatementRunes {
			return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] statement is invalid", index)
		}
		if fact.ConfidenceBasisPoints == 0 || fact.ConfidenceBasisPoints > 10000 {
			return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] confidence is invalid", index)
		}
		if len(fact.EvidenceChunkIDs) == 0 || len(fact.EvidenceChunkIDs) > knowledgeMaxChunksPerBatch {
			return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] evidence is invalid", index)
		}
		seenEvidence := make(map[string]struct{}, len(fact.EvidenceChunkIDs))
		for _, chunkID := range fact.EvidenceChunkIDs {
			if _, ok := allowedChunks[chunkID]; !ok {
				return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] references an unknown chunk", index)
			}
			if _, duplicate := seenEvidence[chunkID]; duplicate {
				return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] repeats evidence", index)
			}
			seenEvidence[chunkID] = struct{}{}
		}
	}
	return output, nil
}

type knowledgeReconcilePromptCandidate struct {
	ID                    string `json:"id"`
	Statement             string `json:"statement"`
	ConfidenceBasisPoints uint16 `json:"confidenceBasisPoints"`
	UpdatedAtUnixMS       int64  `json:"updatedAtUnixMs"`
}

type knowledgeReconcilePromptFact struct {
	Index      int                                 `json:"index"`
	Fact       knowledgeIngestFact                 `json:"fact"`
	Candidates []knowledgeReconcilePromptCandidate `json:"candidates"`
}

type knowledgeReconcilePromptPayload struct {
	BatchID string                         `json:"batchId"`
	Facts   []knowledgeReconcilePromptFact `json:"facts"`
}

type knowledgeReconcileMutation struct {
	FactIndex int    `json:"factIndex"`
	Operation string `json:"operation"`
	MemoryID  string `json:"memoryId,omitempty"`
}

type knowledgeReconcileOutput struct {
	Mutations []knowledgeReconcileMutation `json:"mutations"`
}

func buildKnowledgeReconcileInput(batchID string, facts []memory.KnowledgeIngestFact, recalls []memory.KnowledgeIngestRecall) ([]model.PromptItem, []map[string]string, error) {
	if batchID == "" || len(facts) == 0 || len(recalls) != len(facts) {
		return nil, nil, errors.New("knowledge reconcile input is invalid")
	}
	recallByFact := make(map[int]memory.KnowledgeIngestRecall, len(recalls))
	for _, recall := range recalls {
		if recall.FactIndex < 0 || recall.FactIndex >= len(facts) {
			return nil, nil, errors.New("knowledge reconcile recall index is invalid")
		}
		if _, duplicate := recallByFact[recall.FactIndex]; duplicate {
			return nil, nil, errors.New("knowledge reconcile recall index is duplicated")
		}
		if len(recall.Candidates) > memory.MaxKnowledgeIngestRecallCandidates {
			return nil, nil, errors.New("knowledge reconcile candidate limit exceeded")
		}
		recallByFact[recall.FactIndex] = recall
	}
	promptFacts := make([]knowledgeReconcilePromptFact, len(facts))
	aliases := make([]map[string]string, len(facts))
	for index, fact := range facts {
		recall, exists := recallByFact[index]
		if !exists {
			return nil, nil, errors.New("knowledge reconcile recall is missing")
		}
		aliases[index] = make(map[string]string, len(recall.Candidates))
		candidates := make([]knowledgeReconcilePromptCandidate, 0, len(recall.Candidates))
		seenIDs := make(map[string]struct{}, len(recall.Candidates))
		for candidateIndex, candidate := range recall.Candidates {
			if candidate.ID == "" || strings.TrimSpace(candidate.Statement) == "" {
				return nil, nil, errors.New("knowledge reconcile candidate is invalid")
			}
			if _, duplicate := seenIDs[candidate.ID]; duplicate {
				return nil, nil, errors.New("knowledge reconcile candidate is duplicated")
			}
			seenIDs[candidate.ID] = struct{}{}
			alias := fmt.Sprintf("f%dm%d", index, candidateIndex)
			aliases[index][alias] = candidate.ID
			candidates = append(candidates, knowledgeReconcilePromptCandidate{
				ID: alias, Statement: candidate.Statement,
				ConfidenceBasisPoints: candidate.ConfidenceBasisPoints,
				UpdatedAtUnixMS:       candidate.UpdatedAtUnixMS,
			})
		}
		promptFacts[index] = knowledgeReconcilePromptFact{
			Index: index,
			Fact: knowledgeIngestFact{
				Subject: fact.Subject, Predicate: fact.Predicate, Value: fact.Value,
				Statement: fact.Statement, ConfidenceBasisPoints: fact.ConfidenceBasisPoints,
				EvidenceChunkIDs: append([]string(nil), fact.EvidenceChunkIDs...),
			},
			Candidates: candidates,
		}
	}
	payload, err := json.Marshal(struct {
		FairyContextData knowledgeReconcilePromptPayload `json:"fairy_context_data"`
	}{
		FairyContextData: knowledgeReconcilePromptPayload{BatchID: batchID, Facts: promptFacts},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("serializing knowledge reconcile input: %w", err)
	}
	return []model.PromptItem{{Type: model.PromptItemContextData, Content: string(payload)}}, aliases, nil
}

func parseKnowledgeReconcileOutput(raw string, facts []memory.KnowledgeIngestFact, aliases []map[string]string) ([]memory.KnowledgeIngestMutation, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("knowledge reconcile model returned empty output")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	decoder.DisallowUnknownFields()
	var output knowledgeReconcileOutput
	if err := decoder.Decode(&output); err != nil {
		return nil, errors.New("knowledge reconcile model did not return strict JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("knowledge reconcile model returned trailing content")
	}
	if output.Mutations == nil || len(output.Mutations) != len(facts) || len(aliases) != len(facts) {
		return nil, errors.New("knowledge reconcile must return one mutation per fact")
	}
	mutations := make([]memory.KnowledgeIngestMutation, len(facts))
	seen := make(map[int]struct{}, len(facts))
	for _, mutation := range output.Mutations {
		if mutation.FactIndex < 0 || mutation.FactIndex >= len(facts) {
			return nil, errors.New("knowledge reconcile fact index is invalid")
		}
		if _, duplicate := seen[mutation.FactIndex]; duplicate {
			return nil, errors.New("knowledge reconcile fact index is duplicated")
		}
		seen[mutation.FactIndex] = struct{}{}
		operation := memory.KnowledgeMutationOperation(mutation.Operation)
		resolvedID := ""
		switch operation {
		case memory.KnowledgeMutationAdd:
			if mutation.MemoryID != "" {
				return nil, errors.New("knowledge reconcile ADD must not reference memory")
			}
		case memory.KnowledgeMutationUpdate, memory.KnowledgeMutationDelete, memory.KnowledgeMutationNone:
			var exists bool
			resolvedID, exists = aliases[mutation.FactIndex][mutation.MemoryID]
			if mutation.MemoryID == "" || !exists {
				return nil, errors.New("knowledge reconcile mutation references an unsupplied memory")
			}
		default:
			return nil, errors.New("knowledge reconcile operation is invalid")
		}
		mutations[mutation.FactIndex] = memory.KnowledgeIngestMutation{
			FactIndex: mutation.FactIndex,
			Operation: operation,
			MemoryID:  resolvedID,
		}
	}
	return mutations, nil
}
