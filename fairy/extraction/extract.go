package extraction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"fairy/memory"
	"fairy/model"
)

const (
	Threshold          uint64 = 6
	BatchLimit                = memory.DefaultExtractionBatchLimit
	IdleSeconds               = 30
	EmbeddingPassLimit        = 8
)

type batchPromptPayload struct {
	Type  string                      `json:"type"`
	Input memory.ExtractionBatchInput `json:"input"`
}

func BuildInput(batch memory.ExtractionBatchInput) ([]model.PromptItem, error) {
	payload, err := json.Marshal(struct {
		FairyContextData batchPromptPayload `json:"fairy_context_data"`
	}{
		FairyContextData: batchPromptPayload{Type: "extraction_batch", Input: batch},
	})
	if err != nil {
		return nil, fmt.Errorf("serializing extraction batch: %w", err)
	}
	return []model.PromptItem{{Type: model.PromptItemContextData, Content: string(payload)}}, nil
}

func ParseMutationOutput(raw string) (memory.MemoryMutationOutput, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return memory.MemoryMutationOutput{}, errors.New("extraction model returned empty output")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	decoder.DisallowUnknownFields()
	var output memory.MemoryMutationOutput
	if err := decoder.Decode(&output); err != nil {
		return memory.MemoryMutationOutput{}, errors.New("extraction model did not return strict MemoryMutationOutput JSON")
	}
	if decoder.More() {
		return memory.MemoryMutationOutput{}, errors.New("extraction model returned trailing JSON")
	}
	if len(output.Mutations) > memory.MaxMemoryMutationsPerBatch {
		return memory.MemoryMutationOutput{}, errors.New("extraction batch exceeds memory mutation limit")
	}
	for i := range output.Mutations {
		mutation := output.Mutations[i]
		if mutation.Operation != "create" && mutation.Operation != "supersede" {
			return memory.MemoryMutationOutput{}, fmt.Errorf("unsupported memory mutation operation %q", mutation.Operation)
		}
		if strings.TrimSpace(mutation.SourceTurnID) == "" {
			return memory.MemoryMutationOutput{}, errors.New("memory mutation sourceTurnId is required")
		}
	}
	return output, nil
}
