package extraction

import (
	"errors"
	"fmt"
	"strings"

	"fairy/context/memory/personal"
)

func ValidateMutation(mutation *Mutation, characterID string) error {
	if mutation == nil {
		return errors.New("memory mutation is required")
	}
	switch mutation.Operation {
	case OperationAdd, OperationReplace:
		if mutation.Operation == OperationReplace && invalidID(mutation.MemoryID) {
			return errors.New("memory_id is invalid")
		}
		if mutation.Operation == OperationAdd && mutation.MemoryID != "" {
			return errors.New("ADD must not reference memory_id")
		}
		if err := personal.ValidateInput(mutation.Kind, mutation.Scope, mutation.Content, mutation.ConfidenceBasisPoints); err != nil {
			return err
		}
		if strings.TrimSpace(mutation.Content) != mutation.Content {
			return errors.New("memory mutation content must not include leading or trailing whitespace")
		}
		if mutation.Scope.Type == "unassigned_legacy" {
			return errors.New("automatic extraction cannot create or modify legacy relationship memories")
		}
		if mutation.Kind == "relationship" && (mutation.Scope.Type != "character" || mutation.Scope.CharacterID != characterID) {
			return errors.New("relationship mutation does not belong to the current character")
		}
	case OperationDelete, OperationNone:
		if invalidID(mutation.MemoryID) {
			return errors.New("memory_id is invalid")
		}
		if mutation.Kind != "" || mutation.Scope != (personal.Scope{}) || mutation.Content != "" || mutation.ConfidenceBasisPoints != 0 {
			return errors.New("DELETE and NONE must not contain replacement fields")
		}
	default:
		return errors.New("memory mutation operation must be ADD, REPLACE, DELETE, or NONE")
	}
	if invalidID(mutation.SourceTurnID) {
		return errors.New("source_turn_id is invalid")
	}
	return nil
}

func invalidID(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return true
	}
	for _, character := range value {
		if character == 0 || character < 32 && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}

func ValidateBatchLimit(limit int) error {
	if limit < 1 || limit > DefaultBatchLimit {
		return fmt.Errorf("extraction batch limit must be between 1 and %d", DefaultBatchLimit)
	}
	return nil
}
