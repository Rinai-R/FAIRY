package model

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const MaxPromptImageBytes = 768 << 10

func validatePromptItems(connection Connection, items []PromptItem) error {
	for index, item := range items {
		if item.Content != "" && item.Parts != nil && len(*item.Parts) > 0 {
			return fmt.Errorf("prompt item %d cannot mix legacy content and typed parts", index)
		}
		if err := validateToolFields(item); err != nil {
			return fmt.Errorf("prompt item %d: %w", index, err)
		}
		if item.Parts == nil {
			if item.Type == PromptItemToolResult {
				return fmt.Errorf("prompt item %d: tool result parts are required", index)
			}
			continue
		}
		textParts := 0
		for partIndex, part := range *item.Parts {
			if err := validatePromptContentPart(connection, part); err != nil {
				return fmt.Errorf("prompt item %d part %d: %w", index, partIndex, err)
			}
			if part.Type == PromptContentText {
				textParts++
			}
		}
		if item.Type == PromptItemToolResult && textParts == 0 {
			return fmt.Errorf("prompt item %d: tool result requires a text part", index)
		}
	}
	return nil
}

func promptItemsContainToolResult(items []PromptItem) bool {
	for _, item := range items {
		if item.Type == PromptItemToolResult {
			return true
		}
	}
	return false
}

func assistantPromptContent(item PromptItem, lane PromptLane) (string, error) {
	if lane != PromptLaneRespond {
		return item.Content, nil
	}
	encoded, err := json.Marshal(struct {
		Chains []replyChain `json:"chains"`
	}{
		Chains: []replyChain{{VisualState: "idle", Text: item.Content}},
	})
	if err != nil {
		return "", fmt.Errorf("serializing assistant reply chain history: %w", err)
	}
	return string(encoded), nil
}

func mapResponsesPromptItems(items []PromptItem, lane PromptLane) ([]any, error) {
	input := make([]any, 0, len(items)+1)
	for _, item := range items {
		switch item.Type {
		case PromptItemUserMessage, PromptItemContextData:
			input = append(input, openAIMessage{Role: "user", Content: item.Content})
		case PromptItemAssistantMessage:
			content, err := assistantPromptContent(item, lane)
			if err != nil {
				return nil, err
			}
			input = append(input, openAIMessage{Role: "assistant", Content: content})
		case PromptItemToolCall:
			input = append(input, map[string]any{
				"type": "function_call", "call_id": item.ToolCallID,
				"name": item.ToolName, "arguments": item.ToolArguments,
			})
		case PromptItemToolResult:
			text, hasImage := toolResultTextAndImage(item)
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": item.ToolCallID, "output": text,
			})
			if hasImage {
				input = append(input, openAIMessage{Role: "user", Content: responsesContentParts(*item.Parts)})
			}
		default:
			return nil, fmt.Errorf("prompt item type %q is not supported", item.Type)
		}
	}
	return input, nil
}

func mapChatPromptItems(items []PromptItem, lane PromptLane) ([]openAIMessage, error) {
	messages := make([]openAIMessage, 0, len(items)+1)
	for _, item := range items {
		switch item.Type {
		case PromptItemUserMessage, PromptItemContextData:
			messages = append(messages, openAIMessage{Role: "user", Content: item.Content})
		case PromptItemAssistantMessage:
			content, err := assistantPromptContent(item, lane)
			if err != nil {
				return nil, err
			}
			messages = append(messages, openAIMessage{Role: "assistant", Content: content})
		case PromptItemToolCall:
			messages = append(messages, openAIMessage{Role: "assistant", ToolCalls: []chatToolCall{{
				ID: item.ToolCallID, Type: "function",
				Function: chatToolFunction{Name: item.ToolName, Arguments: item.ToolArguments},
			}}})
		case PromptItemToolResult:
			text, hasImage := toolResultTextAndImage(item)
			messages = append(messages, openAIMessage{Role: "tool", ToolCallID: item.ToolCallID, Content: text})
			if hasImage {
				messages = append(messages, openAIMessage{Role: "user", Content: chatContentParts(*item.Parts)})
			}
		default:
			return nil, fmt.Errorf("prompt item type %q is not supported", item.Type)
		}
	}
	return messages, nil
}

func toolResultTextAndImage(item PromptItem) (string, bool) {
	var text strings.Builder
	hasImage := false
	for _, part := range *item.Parts {
		switch part.Type {
		case PromptContentText:
			if text.Len() > 0 {
				text.WriteByte('\n')
			}
			text.WriteString(part.Text)
		case PromptContentImage:
			hasImage = true
		}
	}
	return text.String(), hasImage
}

func responsesContentParts(parts PromptContentParts) []map[string]any {
	content := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if part.Type == PromptContentText {
			content = append(content, map[string]any{"type": "input_text", "text": part.Text})
		} else {
			content = append(content, map[string]any{"type": "input_image", "image_url": part.ImageDataURL, "detail": "auto"})
		}
	}
	return content
}

func chatContentParts(parts PromptContentParts) []map[string]any {
	content := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if part.Type == PromptContentText {
			content = append(content, map[string]any{"type": "text", "text": part.Text})
		} else {
			content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": part.ImageDataURL, "detail": "auto"}})
		}
	}
	return content
}

func validateToolFields(item PromptItem) error {
	switch item.Type {
	case PromptItemToolCall:
		if err := validateToolCallID(item.ToolCallID); err != nil {
			return err
		}
		if strings.TrimSpace(item.ToolName) == "" || strings.TrimSpace(item.ToolName) != item.ToolName {
			return errors.New("tool call name is required")
		}
		if item.ToolArguments == "" {
			return errors.New("tool call arguments are required")
		}
	case PromptItemToolResult:
		if err := validateToolCallID(item.ToolCallID); err != nil {
			return err
		}
		if item.ToolName != "" || item.ToolArguments != "" {
			return errors.New("tool result cannot declare a tool name or arguments")
		}
	default:
		if item.ToolCallID != "" || item.ToolName != "" || item.ToolArguments != "" {
			return errors.New("non-tool item cannot declare tool fields")
		}
	}
	return nil
}

func validateToolCallID(value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return errors.New("tool call ID is required")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return errors.New("tool call ID is invalid")
		}
	}
	return nil
}

func validatePromptContentPart(connection Connection, part PromptContentPart) error {
	switch part.Type {
	case PromptContentText:
		if part.Text == "" {
			return errors.New("text part is empty")
		}
		if part.ImageDataURL != "" || part.ImageMIME != "" || part.ImagePurpose != "" {
			return errors.New("text part contains image fields")
		}
		return nil
	case PromptContentImage:
		if !connection.Capabilities.VisionInput {
			return errors.New("model connection does not support image input")
		}
		if part.Text != "" {
			return errors.New("image part contains text")
		}
		if part.ImageMIME != "image/png" && part.ImageMIME != "image/jpeg" {
			return errors.New("image MIME type is not supported")
		}
		if strings.TrimSpace(part.ImagePurpose) == "" || strings.TrimSpace(part.ImagePurpose) != part.ImagePurpose {
			return errors.New("image purpose is required")
		}
		prefix := "data:" + part.ImageMIME + ";base64,"
		if !strings.HasPrefix(part.ImageDataURL, prefix) {
			return errors.New("image data URL does not match its MIME type")
		}
		encoded := strings.TrimPrefix(part.ImageDataURL, prefix)
		if encoded == "" || base64.StdEncoding.DecodedLen(len(encoded)) > MaxPromptImageBytes {
			return errors.New("image exceeds the byte limit")
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return errors.New("image data URL is invalid")
		}
		if len(decoded) == 0 || len(decoded) > MaxPromptImageBytes {
			return errors.New("image exceeds the byte limit")
		}
		return nil
	default:
		return fmt.Errorf("content part type %q is not supported", part.Type)
	}
}
