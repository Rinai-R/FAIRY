package transcript

import (
	"strings"

	historyexpr "fairy/context/history/expression"
)

const (
	stickerHistoryPrefix = "[表情包："
	stickerHistorySuffix = "]"
)

// PromptMessageText projects a persisted message into bounded textual history.
// Sticker IDs, MIME types, bytes, and current asset state never enter prompts.
func PromptMessageText(message MessageRecord) string {
	if message.Role != "assistant" || len(message.Parts) == 0 {
		return message.Content
	}
	parts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		switch part.Kind {
		case historyexpr.Utterance:
			parts = append(parts, part.Text)
		case historyexpr.Sticker:
			if part.Sticker != nil {
				parts = append(parts, stickerHistoryPrefix+part.Sticker.Description+stickerHistorySuffix)
			}
		}
	}
	return strings.Join(parts, "\n")
}

type expressionValidator struct{}

func (expressionValidator) ValidateID(label string, value string) error {
	return ValidateID(label, value)
}

func (expressionValidator) ValidateContent(label string, value string) error {
	return ValidateContent(label, value)
}

func (expressionValidator) ContainsDisallowedControl(value string) bool {
	return ContainsDisallowedControl(value)
}

func validateExpressionMessage(content string, parts []historyexpr.Part) error {
	return historyexpr.Validate(content, parts, expressionValidator{})
}
