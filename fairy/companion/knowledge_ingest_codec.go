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
	seenSlots := make(map[string]struct{}, len(output.Facts))
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
		slot := strings.ToLower(fact.Subject) + "\x00" + strings.ToLower(fact.Predicate)
		if _, duplicate := seenSlots[slot]; duplicate {
			return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] duplicates a fact slot", index)
		}
		seenSlots[slot] = struct{}{}
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
