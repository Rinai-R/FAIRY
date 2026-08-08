package expression

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxParts                   = 12
	MaxStickerDescriptionRunes = 512
)

type Validator interface {
	ValidateID(label string, value string) error
	ValidateContent(label string, value string) error
	ContainsDisallowedControl(value string) bool
}

func Validate(content string, parts []Part, validator Validator) error {
	if validator == nil {
		return errors.New("expression validator is required")
	}
	if len(parts) == 0 {
		return validator.ValidateContent("assistant message", content)
	}
	if len(parts) > MaxParts {
		return fmt.Errorf("assistant expression parts exceed %d", MaxParts)
	}
	stickerCount := 0
	for index, part := range parts {
		if strings.TrimSpace(part.VisualState) == "" || validator.ContainsDisallowedControl(part.VisualState) {
			return fmt.Errorf("assistant expression part %d visual state is invalid", index)
		}
		switch part.Kind {
		case Utterance:
			if err := validator.ValidateContent("assistant utterance", part.Text); err != nil {
				return fmt.Errorf("assistant expression part %d: %w", index, err)
			}
			if part.Sticker != nil {
				return fmt.Errorf("assistant expression part %d utterance contains sticker", index)
			}
		case Sticker:
			stickerCount++
			if stickerCount > 1 {
				return errors.New("assistant expression parts contain more than one sticker")
			}
			if part.Text != "" || part.Sticker == nil {
				return fmt.Errorf("assistant expression part %d sticker shape is invalid", index)
			}
			if err := validateStickerSnapshot(*part.Sticker, validator); err != nil {
				return fmt.Errorf("assistant expression part %d: %w", index, err)
			}
		default:
			return fmt.Errorf("assistant expression part %d kind is invalid", index)
		}
	}
	if projection := TextProjection(parts); projection != content {
		return errors.New("assistant message content does not match expression text projection")
	}
	return nil
}

func validateStickerSnapshot(snapshot StickerSnapshot, validator Validator) error {
	if err := validator.ValidateID("sticker id", snapshot.ID); err != nil {
		return err
	}
	if snapshot.Description == "" || strings.TrimSpace(snapshot.Description) != snapshot.Description ||
		validator.ContainsDisallowedControl(snapshot.Description) || utf8.RuneCountInString(snapshot.Description) > MaxStickerDescriptionRunes {
		return errors.New("sticker description snapshot is invalid")
	}
	switch snapshot.MIMEType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return nil
	default:
		return errors.New("sticker MIME snapshot is invalid")
	}
}

func TextProjection(parts []Part) string {
	text := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Kind == Utterance {
			text = append(text, part.Text)
		}
	}
	return strings.Join(text, "\n")
}
