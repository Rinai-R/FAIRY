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
	if mutation.Operation != "create" && mutation.Operation != "supersede" {
		return errors.New("memory mutation operation must be create or supersede")
	}
	if mutation.Operation == "supersede" && invalidID(mutation.MemoryID) {
		return errors.New("memory_id is invalid")
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
