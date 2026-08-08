package learning

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"fairy/context/memory/extraction"
	"fairy/runtime/model"
)

const (
	extractionThreshold   uint64 = 6
	extractionBatchLimit         = extraction.DefaultBatchLimit
	extractionIdleSeconds        = 30
)

type batchPromptPayload struct {
	Type  string                `json:"type"`
	Input extraction.BatchInput `json:"input"`
}

func buildExtractInput(batch extraction.BatchInput) ([]model.PromptItem, error) {
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

func parseMemoryMutationOutput(raw string) (extraction.MutationOutput, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return extraction.MutationOutput{}, errors.New("extraction model returned empty output")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	decoder.DisallowUnknownFields()
	var output extraction.MutationOutput
	if err := decoder.Decode(&output); err != nil {
		return extraction.MutationOutput{}, errors.New("extraction model did not return strict MemoryMutationOutput JSON")
	}
	if decoder.More() {
		return extraction.MutationOutput{}, errors.New("extraction model returned trailing JSON")
	}
	if len(output.Mutations) > extraction.MaxMutations {
		return extraction.MutationOutput{}, errors.New("extraction batch exceeds memory mutation limit")
	}
	for i := range output.Mutations {
		mutation := output.Mutations[i]
		if mutation.Operation != "create" && mutation.Operation != "supersede" {
			return extraction.MutationOutput{}, fmt.Errorf("unsupported memory mutation operation %q", mutation.Operation)
		}
		if strings.TrimSpace(mutation.SourceTurnID) == "" {
			return extraction.MutationOutput{}, errors.New("memory mutation sourceTurnId is required")
		}
	}
	return output, nil
}
