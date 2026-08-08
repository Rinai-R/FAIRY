package personal

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

func ValidateInput(kind string, scope Scope, content string, confidence uint16) error {
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
	if scope.Type == "character" && invalidIdentifier(scope.CharacterID) {
		return errors.New("character_id is invalid")
	}
	if err := ValidateContent(content); err != nil {
		return err
	}
	if confidence > 10000 {
		return errors.New("memory confidence is invalid")
	}
	return nil
}

func ValidateContent(content string) error {
	if strings.TrimSpace(content) == "" || containsDisallowedControl(content) {
		return errors.New("memory content is invalid")
	}
	if utf8.RuneCountInString(content) > MaxContentRunes {
		return fmt.Errorf("memory content exceeds %d Unicode characters", MaxContentRunes)
	}
	return nil
}

func ValidatePersistedContent(id, content string) error {
	if utf8.RuneCountInString(content) > MaxContentRunes {
		return fmt.Errorf("personal memory %s content exceeds %d Unicode characters", id, MaxContentRunes)
	}
	return nil
}

func NormalizeContent(content string) string {
	return strings.Join(strings.Fields(content), " ")
}

func ScopeColumns(scope Scope) (string, *string, string) {
	if scope.Type == "character" {
		return "character", &scope.CharacterID, "ready"
	}
	if scope.Type == "unassigned_legacy" {
		return "unassigned_legacy", nil, "needs_review"
	}
	return "global", nil, "ready"
}

func Layer(kind string, scope Scope) string {
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

func invalidIdentifier(value string) bool {
	return value == "" || strings.TrimSpace(value) != value || containsDisallowedControl(value)
}

func containsDisallowedControl(value string) bool {
	for _, character := range value {
		if character == 0 || character < 32 && character != '\n' && character != '\r' && character != '\t' {
			return true
		}
	}
	return false
}
