package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	contracts "fairy/contracts/interaction"
	mempostgres "fairy/internal/adapters/memory/postgres"
	domainmemory "fairy/internal/domain/memory"
)

const (
	maxResultsPerKind        = 4
	maxRetrievedContextChars = MaxPersonalMemoryContentRunes
	maxFTSQueryChars         = domainmemory.MaxFTSQueryChars
)

func validateID(label string, value string) error {
	return domainmemory.ValidateID(label, value)
}

func validateContent(label string, value string) error {
	return domainmemory.ValidateContent(label, value)
}

func validateEndpointConversationKey(characterID string, binding contracts.Binding, digest string) error {
	if err := validateID("character_id", characterID); err != nil {
		return err
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	if err := contracts.ValidateDigest(digest); err != nil {
		return errors.New("endpoint key digest is invalid")
	}
	return nil
}

func containsDisallowedControl(value string) bool {
	return domainmemory.ContainsDisallowedControl(value)
}

func validateMemoryInput(kind string, scope MemoryScope, content string, confidence uint16) error {
	return domainmemory.ValidateMemoryInput(kind, scope, content, confidence)
}

func validatePersonalMemoryContent(content string) error {
	return domainmemory.ValidatePersonalMemoryContent(content)
}

func validatePersistedPersonalMemoryContent(id, content string) error {
	return domainmemory.ValidatePersistedPersonalMemoryContent(id, content)
}

func memoryScopeColumns(scope MemoryScope) (string, *string, string) {
	return domainmemory.MemoryScopeColumns(scope)
}

func buildExtractionRetrievalProjection(turns []ExtractionTurn) []string {
	return domainmemory.BuildExtractionRetrievalProjection(turns)
}

func validateMemoryMutation(mutation *MemoryMutation, characterID string) error {
	return domainmemory.ValidateMemoryMutation(mutation, characterID)
}

func normalizeMemoryContent(content string) string {
	return domainmemory.NormalizeMemoryContent(content)
}

func validateSocialMemoryBatch(input SocialMemoryBatchInput) error {
	return domainmemory.ValidateSocialMemoryBatch(input)
}

func validateSocialReplyFeedback(input SocialReplyFeedbackInput) error {
	return domainmemory.ValidateSocialReplyFeedback(input)
}

func validSocialMemoryKind(kind string) bool {
	return domainmemory.ValidSocialMemoryKind(kind)
}

func validateSocialText(name, value string, limit int) error {
	return domainmemory.ValidateSocialText(name, value, limit)
}

func validateSocialPersonNoteInput(input SocialPersonNoteInput) error {
	if err := validateID("character_id", input.CharacterID); err != nil {
		return err
	}
	if err := validateID("conversation_id", input.ConversationID); err != nil {
		return err
	}
	if err := validateID("sender_id", input.SenderID); err != nil {
		return err
	}
	if name := strings.TrimSpace(input.SenderName); name != "" {
		if utf8.RuneCountInString(name) > 80 {
			return errors.New("social person sender_name must not exceed 80 runes")
		}
		for _, r := range name {
			if unicode.IsControl(r) {
				return errors.New("social person sender_name contains control characters")
			}
		}
	}
	note := strings.TrimSpace(input.Note)
	if note == "" {
		return errors.New("social person note is required")
	}
	if utf8.RuneCountInString(note) > MaxSocialPersonNoteRunes {
		return fmt.Errorf("social person note must not exceed %d runes", MaxSocialPersonNoteRunes)
	}
	for _, r := range note {
		if unicode.IsControl(r) {
			return errors.New("social person note contains control characters")
		}
	}
	return nil
}

func buildFTSQuery(query string) (string, error) {
	return mempostgres.BuildFTSQuery(query)
}

func semanticQueryText(query string) string {
	trimmed := strings.TrimSpace(query)
	if len([]rune(trimmed)) < 2 {
		return ""
	}
	return trimmed
}

func personalMemoryLayer(kind string, scope MemoryScope) string {
	return domainmemory.PersonalMemoryLayer(kind, scope)
}

func semanticContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
