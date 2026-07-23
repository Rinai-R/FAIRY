package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxSocialPersonNoteRunes = 240
	maxSocialPersonNotes     = 8
)

type SocialPersonNoteInput struct {
	CharacterID    string
	ConversationID string
	SenderID       string
	SenderName     string
	Note           string
}

type SocialPersonNote struct {
	ID             string
	CharacterID    string
	ConversationID string
	SenderID       string
	SenderName     string
	Note           string
	UpdatedAtUnixMS int64
}

func (s *Store) UpsertSocialPersonNote(ctx context.Context, input SocialPersonNoteInput) (SocialPersonNote, error) {
	if s == nil || s.pool == nil {
		return SocialPersonNote{}, ErrDatabasePoolEmpty
	}
	if err := validateSocialPersonNoteInput(input); err != nil {
		return SocialPersonNote{}, err
	}
	return s.upsertSocialPersonNotePostgres(ctx, input)
}

func (s *Store) ListSocialPersonNotes(ctx context.Context, characterID, conversationID string, senderIDs []string) ([]SocialPersonNote, error) {
	if s == nil || s.pool == nil {
		return nil, ErrDatabasePoolEmpty
	}
	if err := validateID("character_id", characterID); err != nil {
		return nil, err
	}
	if err := validateID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	cleanIDs := make([]string, 0, len(senderIDs))
	seen := make(map[string]struct{}, len(senderIDs))
	for _, id := range senderIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if err := validateID("sender_id", id); err != nil {
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
	return s.listSocialPersonNotesPostgres(ctx, characterID, conversationID, cleanIDs)
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
