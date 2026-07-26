package memory

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

func ValidateID(label string, value string) error {
	if value == "" || strings.TrimSpace(value) != value || ContainsDisallowedControl(value) {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func ValidateContent(label string, value string) error {
	if value == "" || strings.TrimSpace(value) == "" || ContainsDisallowedControl(value) {
		return fmt.Errorf("%s is invalid", label)
	}
	return nil
}

func ContainsDisallowedControl(value string) bool {
	for _, character := range value {
		if character == 0 || character < 32 && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}

func ValidateMemoryInput(kind string, scope MemoryScope, content string, confidence uint16) error {
	if kind != "preference" && kind != "profile" && kind != "relationship" && kind != "experience" {
		return errors.New("memory kind is unsupported")
	}
	if kind == "relationship" {
		if scope.Type != "character" && scope.Type != "unassigned_legacy" {
			return errors.New("relationship memory requires character or legacy scope")
		}
	} else if scope.Type != "global" {
		return errors.New("non-relationship memory requires global scope")
	}
	if scope.Type == "character" {
		if err := ValidateID("character_id", scope.CharacterID); err != nil {
			return err
		}
	}
	if err := ValidatePersonalMemoryContent(content); err != nil {
		return err
	}
	if confidence > 10000 {
		return errors.New("memory confidence is invalid")
	}
	return nil
}

func ValidatePersonalMemoryContent(content string) error {
	if err := ValidateContent("memory content", content); err != nil {
		return err
	}
	if utf8.RuneCountInString(content) > MaxPersonalMemoryContentRunes {
		return fmt.Errorf("memory content exceeds %d Unicode characters", MaxPersonalMemoryContentRunes)
	}
	return nil
}

func ValidatePersistedPersonalMemoryContent(id, content string) error {
	if utf8.RuneCountInString(content) > MaxPersonalMemoryContentRunes {
		return fmt.Errorf("personal memory %s content exceeds %d Unicode characters", id, MaxPersonalMemoryContentRunes)
	}
	return nil
}

func ValidateMemoryMutation(mutation *MemoryMutation, characterID string) error {
	if mutation == nil {
		return errors.New("memory mutation is required")
	}
	if mutation.Operation != "create" && mutation.Operation != "supersede" {
		return errors.New("memory mutation operation must be create or supersede")
	}
	if mutation.Operation == "supersede" {
		if err := ValidateID("memory_id", mutation.MemoryID); err != nil {
			return err
		}
	}
	if err := ValidateMemoryInput(mutation.Kind, mutation.Scope, mutation.Content, mutation.ConfidenceBasisPoints); err != nil {
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

func NormalizeMemoryContent(content string) string {
	return strings.Join(strings.Fields(content), " ")
}

func MemoryScopeColumns(scope MemoryScope) (string, *string, string) {
	if scope.Type == "character" {
		return "character", &scope.CharacterID, "ready"
	}
	if scope.Type == "unassigned_legacy" {
		return "unassigned_legacy", nil, "needs_review"
	}
	return "global", nil, "ready"
}

func PersonalMemoryLayer(kind string, scope MemoryScope) string {
	switch strings.TrimSpace(kind) {
	case "profile":
		return "profile"
	case "preference":
		return "preference"
	case "experience":
		return "experience"
	case "relationship":
		return "relationship"
	}
	if scope.Type == "character" {
		return "relationship"
	}
	return "memory"
}
