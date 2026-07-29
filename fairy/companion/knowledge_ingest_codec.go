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
	maxKnowledgeIngestTopicRunes     = 300
	maxKnowledgeIngestStatementRunes = 1200
)

type knowledgeIngestFact struct {
	Topic                 string   `json:"topic"`
	Statement             string   `json:"statement"`
	ConfidenceBasisPoints uint16   `json:"confidenceBasisPoints"`
	SourceHitIDs          []string `json:"sourceHitIDs"`
}

type knowledgeIngestOutput struct {
	Facts []knowledgeIngestFact `json:"facts"`
}

type knowledgeIngestPromptSource struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	URL             string `json:"url"`
	Snippet         string `json:"snippet"`
	Rank            uint8  `json:"rank"`
	FetchedAtUnixMS int64  `json:"fetchedAtUnixMs"`
}

type knowledgeIngestPromptBatch struct {
	BatchID  string                        `json:"batchId"`
	Category string                        `json:"category"`
	Sources  []knowledgeIngestPromptSource `json:"sources"`
}

func buildKnowledgeIngestInput(batch memory.KnowledgeIngestBatch) ([]model.PromptItem, error) {
	if batch.ID == "" || batch.Category == "" || len(batch.Sources) == 0 || len(batch.Sources) > memory.MaxKnowledgeIngestSources {
		return nil, errors.New("knowledge ingest batch is invalid")
	}
	sources := make([]knowledgeIngestPromptSource, 0, len(batch.Sources))
	for _, source := range batch.Sources {
		sources = append(sources, knowledgeIngestPromptSource{
			ID: source.ID, Title: source.Title, URL: source.URL, Snippet: source.Snippet,
			Rank: source.Rank, FetchedAtUnixMS: source.FetchedAtUnixMS,
		})
	}
	payload, err := json.Marshal(struct {
		FairyContextData knowledgeIngestPromptBatch `json:"fairy_context_data"`
	}{
		FairyContextData: knowledgeIngestPromptBatch{
			BatchID: batch.ID, Category: batch.Category, Sources: sources,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("serializing knowledge ingest batch: %w", err)
	}
	return []model.PromptItem{{Type: model.PromptItemContextData, Content: string(payload)}}, nil
}

func parseKnowledgeIngestOutput(raw string, batch memory.KnowledgeIngestBatch) (knowledgeIngestOutput, error) {
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
	allowedSources := make(map[string]struct{}, len(batch.Sources))
	for _, source := range batch.Sources {
		allowedSources[source.ID] = struct{}{}
	}
	seenStatements := make(map[string]struct{}, len(output.Facts))
	for index := range output.Facts {
		fact := &output.Facts[index]
		if strings.TrimSpace(fact.Topic) != fact.Topic || fact.Topic == "" || utf8.RuneCountInString(fact.Topic) > maxKnowledgeIngestTopicRunes {
			return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] topic is invalid", index)
		}
		if strings.TrimSpace(fact.Statement) != fact.Statement || utf8.RuneCountInString(fact.Statement) < 8 || utf8.RuneCountInString(fact.Statement) > maxKnowledgeIngestStatementRunes {
			return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] statement is invalid", index)
		}
		if fact.ConfidenceBasisPoints == 0 || fact.ConfidenceBasisPoints > 10000 {
			return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] confidence is invalid", index)
		}
		if len(fact.SourceHitIDs) == 0 || len(fact.SourceHitIDs) > memory.MaxKnowledgeIngestSources {
			return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] sources are invalid", index)
		}
		if _, duplicate := seenStatements[fact.Statement]; duplicate {
			return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] duplicates a statement", index)
		}
		seenStatements[fact.Statement] = struct{}{}
		seenSources := make(map[string]struct{}, len(fact.SourceHitIDs))
		for _, sourceID := range fact.SourceHitIDs {
			if _, ok := allowedSources[sourceID]; !ok {
				return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] references an unknown source", index)
			}
			if _, duplicate := seenSources[sourceID]; duplicate {
				return knowledgeIngestOutput{}, fmt.Errorf("knowledge ingest fact[%d] repeats a source", index)
			}
			seenSources[sourceID] = struct{}{}
		}
	}
	return output, nil
}
