package social

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (s *Store) UpsertSocialPersonNote(ctx context.Context, input SocialPersonNoteInput) (SocialPersonNote, error) {
	if err := validateSocialPersonNoteInput(input); err != nil {
		return SocialPersonNote{}, err
	}
	if !s.usesSeekDB() {
		return SocialPersonNote{}, ErrStoreBackendUnavailable
	}
	return s.upsertSocialPersonNoteSeekDB(ctx, input)
}

func (s *Store) ListSocialPersonNotes(ctx context.Context, characterID, conversationID string, senderIDs []string) ([]SocialPersonNote, error) {
	if err := ValidateID("character_id", characterID); err != nil {
		return nil, err
	}
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	cleanIDs := make([]string, 0, len(senderIDs))
	seen := make(map[string]struct{}, len(senderIDs))
	for _, id := range senderIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if err := ValidateID("sender_id", id); err != nil {
			return nil, err
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		cleanIDs = append(cleanIDs, id)
		if len(cleanIDs) >= maxSocialPersonNotes {
			break
		}
	}
	if len(cleanIDs) == 0 {
		return []SocialPersonNote{}, nil
	}
	if !s.usesSeekDB() {
		return nil, ErrStoreBackendUnavailable
	}
	return s.listSocialPersonNotesSeekDB(ctx, characterID, conversationID, cleanIDs)
}

const MaxSocialPersonNoteRunes = 240

func validateSocialPersonNoteInput(input SocialPersonNoteInput) error {
	if err := ValidateID("character_id", input.CharacterID); err != nil {
		return err
	}
	if err := ValidateID("conversation_id", input.ConversationID); err != nil {
		return err
	}
	if err := ValidateID("sender_id", input.SenderID); err != nil {
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
