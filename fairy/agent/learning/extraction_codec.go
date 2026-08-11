package learning

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"fairy/context/memory/extraction"
	"fairy/context/memory/personal"
	"fairy/runtime/model"
)

const (
	extractionThreshold   uint64 = 6
	extractionBatchLimit         = extraction.DefaultBatchLimit
	extractionIdleSeconds        = 30
)

type promptMemory struct {
	Alias                 string         `json:"memoryId"`
	Kind                  string         `json:"kind"`
	Scope                 personal.Scope `json:"scope"`
	Content               string         `json:"content"`
	ConfidenceBasisPoints uint16         `json:"confidenceBasisPoints"`
}

type extractionPromptInput struct {
	BatchID          string               `json:"batchId"`
	ConversationID   string               `json:"conversationId"`
	CharacterID      string               `json:"characterId"`
	Turns            []extraction.Turn    `json:"turns"`
	Candidates       []discoveryCandidate `json:"candidates"`
	ExistingMemories []promptMemory       `json:"existingMemories"`
}

type memoryAliasSet struct {
	byAlias map[string]personal.Retrieved
}

type batchPromptPayload struct {
	Type  string                `json:"type"`
	Input extractionPromptInput `json:"input"`
}

func buildExtractInput(batch extraction.BatchInput, candidates []discoveryCandidate) ([]model.PromptItem, memoryAliasSet, error) {
	aliases := memoryAliasSet{byAlias: make(map[string]personal.Retrieved, len(batch.ExistingMemories))}
	promptMemories := make([]promptMemory, 0, len(batch.ExistingMemories))
	for index, memory := range batch.ExistingMemories {
		alias := fmt.Sprintf("m%d", index)
		aliases.byAlias[alias] = memory
		promptMemories = append(promptMemories, promptMemory{
			Alias: alias, Kind: memory.Kind, Scope: memory.Scope, Content: memory.Content,
			ConfidenceBasisPoints: memory.ConfidenceBasisPoints,
		})
	}
	promptInput := extractionPromptInput{
		BatchID: batch.BatchID, ConversationID: batch.ConversationID, CharacterID: batch.CharacterID,
		Turns: batch.Turns, Candidates: candidates, ExistingMemories: promptMemories,
	}
	payload, err := json.Marshal(struct {
		FairyContextData batchPromptPayload `json:"fairy_context_data"`
	}{FairyContextData: batchPromptPayload{Type: "personal_memory_reconcile", Input: promptInput}})
	if err != nil {
		return nil, memoryAliasSet{}, fmt.Errorf("serializing extraction batch: %w", err)
	}
	return []model.PromptItem{{Type: model.PromptItemContextData, Content: string(payload)}}, aliases, nil
}

type mutationWire struct {
	Operation             string          `json:"operation"`
	SourceTurnID          string          `json:"sourceTurnId"`
	MemoryID              *string         `json:"memoryId,omitempty"`
	Kind                  *string         `json:"kind,omitempty"`
	Scope                 *personal.Scope `json:"scope,omitempty"`
	Content               *string         `json:"content,omitempty"`
	ConfidenceBasisPoints *uint16         `json:"confidenceBasisPoints,omitempty"`
}

func parseMemoryMutationOutput(raw string, aliases memoryAliasSet, allowedTurnIDs map[string]struct{}) (extraction.MutationOutput, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return extraction.MutationOutput{}, errors.New("extraction model returned empty output")
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	decoder.DisallowUnknownFields()
	var wire struct {
		Mutations []mutationWire `json:"mutations"`
	}
	if err := decoder.Decode(&wire); err != nil {
		return extraction.MutationOutput{}, errors.New("extraction model did not return strict MemoryMutationOutput JSON")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return extraction.MutationOutput{}, errors.New("extraction model returned trailing JSON")
	}
	if len(wire.Mutations) > extraction.MaxMutations {
		return extraction.MutationOutput{}, errors.New("extraction batch exceeds memory mutation limit")
	}
	output := extraction.MutationOutput{Mutations: make([]extraction.Mutation, 0, len(wire.Mutations))}
	targets := make(map[string]struct{}, len(wire.Mutations))
	for _, action := range wire.Mutations {
		if _, ok := allowedTurnIDs[action.SourceTurnID]; !ok {
			return extraction.MutationOutput{}, errors.New("memory mutation sourceTurnId was not supplied")
		}
		mutation := extraction.Mutation{Operation: action.Operation, SourceTurnID: action.SourceTurnID}
		switch action.Operation {
		case extraction.OperationAdd:
			if action.MemoryID != nil || action.Kind == nil || action.Scope == nil || action.Content == nil || action.ConfidenceBasisPoints == nil {
				return extraction.MutationOutput{}, errors.New("ADD fields are invalid")
			}
			mutation.Kind, mutation.Scope, mutation.Content, mutation.ConfidenceBasisPoints = *action.Kind, *action.Scope, *action.Content, *action.ConfidenceBasisPoints
		case extraction.OperationReplace:
			if action.MemoryID == nil || action.Kind == nil || action.Scope == nil || action.Content == nil || action.ConfidenceBasisPoints == nil {
				return extraction.MutationOutput{}, errors.New("REPLACE fields are invalid")
			}
			memory, ok := aliases.byAlias[*action.MemoryID]
			if !ok {
				return extraction.MutationOutput{}, errors.New("REPLACE references an unknown memory alias")
			}
			mutation.MemoryID = memory.ID
			mutation.Kind, mutation.Scope, mutation.Content, mutation.ConfidenceBasisPoints = *action.Kind, *action.Scope, *action.Content, *action.ConfidenceBasisPoints
		case extraction.OperationDelete, extraction.OperationNone:
			if action.MemoryID == nil || action.Kind != nil || action.Scope != nil || action.Content != nil || action.ConfidenceBasisPoints != nil {
				return extraction.MutationOutput{}, fmt.Errorf("%s fields are invalid", action.Operation)
			}
			memory, ok := aliases.byAlias[*action.MemoryID]
			if !ok {
				return extraction.MutationOutput{}, fmt.Errorf("%s references an unknown memory alias", action.Operation)
			}
			mutation.MemoryID = memory.ID
		default:
			return extraction.MutationOutput{}, fmt.Errorf("unsupported memory mutation operation %q", action.Operation)
		}
		if mutation.MemoryID != "" {
			if _, exists := targets[mutation.MemoryID]; exists {
				return extraction.MutationOutput{}, errors.New("memory alias is targeted more than once")
			}
			targets[mutation.MemoryID] = struct{}{}
		}
		output.Mutations = append(output.Mutations, mutation)
	}
	return output, nil
}
