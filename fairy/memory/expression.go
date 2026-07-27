package memory

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxExpressionParts         = 12
	MaxStickerDescriptionRunes = 512
	stickerHistoryPrefix       = "[表情包："
	stickerHistorySuffix       = "]"
)

func ValidateExpressionMessage(content string, parts []ExpressionPart) error {
	if len(parts) == 0 {
		return ValidateContent("assistant message", content)
	}
	if len(parts) > MaxExpressionParts {
		return fmt.Errorf("assistant expression parts exceed %d", MaxExpressionParts)
	}
	stickerCount := 0
	for index, part := range parts {
		if strings.TrimSpace(part.VisualState) == "" || ContainsDisallowedControl(part.VisualState) {
			return fmt.Errorf("assistant expression part %d visual state is invalid", index)
		}
		switch part.Kind {
		case ExpressionUtterance:
			if err := ValidateContent("assistant utterance", part.Text); err != nil {
				return fmt.Errorf("assistant expression part %d: %w", index, err)
			}
			if part.Sticker != nil {
				return fmt.Errorf("assistant expression part %d utterance contains sticker", index)
			}
		case ExpressionSticker:
			stickerCount++
			if stickerCount > 1 {
				return errors.New("assistant expression parts contain more than one sticker")
			}
			if part.Text != "" || part.Sticker == nil {
				return fmt.Errorf("assistant expression part %d sticker shape is invalid", index)
			}
			if err := validateStickerSnapshot(*part.Sticker); err != nil {
				return fmt.Errorf("assistant expression part %d: %w", index, err)
			}
		default:
			return fmt.Errorf("assistant expression part %d kind is invalid", index)
		}
	}
	if projection := ExpressionTextProjection(parts); projection != content {
		return errors.New("assistant message content does not match expression text projection")
	}
	return nil
}

func validateStickerSnapshot(snapshot StickerSnapshot) error {
	if err := ValidateID("sticker id", snapshot.ID); err != nil {
		return err
	}
	if snapshot.Description == "" || strings.TrimSpace(snapshot.Description) != snapshot.Description ||
		ContainsDisallowedControl(snapshot.Description) || utf8.RuneCountInString(snapshot.Description) > MaxStickerDescriptionRunes {
		return errors.New("sticker description snapshot is invalid")
	}
	switch snapshot.MIMEType {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return nil
	default:
		return errors.New("sticker MIME snapshot is invalid")
	}
}

func ExpressionTextProjection(parts []ExpressionPart) string {
	text := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Kind == ExpressionUtterance {
			text = append(text, part.Text)
		}
	}
	return strings.Join(text, "\n")
}

// PromptMessageText projects a persisted message into bounded textual history.
// Sticker IDs, MIME types, bytes, and current asset state never enter prompts.
func PromptMessageText(message MessageRecord) string {
	if message.Role != "assistant" || len(message.Parts) == 0 {
		return message.Content
	}
	parts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		switch part.Kind {
		case ExpressionUtterance:
			parts = append(parts, part.Text)
		case ExpressionSticker:
			if part.Sticker != nil {
				parts = append(parts, stickerHistoryPrefix+part.Sticker.Description+stickerHistorySuffix)
			}
		}
	}
	return strings.Join(parts, "\n")
}
