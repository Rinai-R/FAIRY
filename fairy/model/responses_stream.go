package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// responsesStreamState accumulates Responses SSE the same way as
// crates/fairy-model-openai/src/response_stream.rs.
type responsesStreamState struct {
	output string
}

func (s *responsesStreamState) handle(payload string, onEvent func(StreamEvent)) (done bool, err error) {
	var event struct {
		Type     string          `json:"type"`
		Delta    string          `json:"delta"`
		Response json.RawMessage `json:"response"`
		Error    *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(payload))
	if err := decoder.Decode(&event); err != nil {
		return false, fmt.Errorf("parsing responses SSE payload: %w", err)
	}
	if event.Error != nil && event.Type == "" {
		if onEvent != nil {
			onEvent(StreamEvent{Type: "failed", Data: event.Error.Message})
		}
		return false, nil
	}
	switch event.Type {
	case "response.output_text.delta", "response.refusal.delta":
		if event.Delta == "" {
			return false, nil
		}
		s.output += event.Delta
		if onEvent != nil {
			onEvent(StreamEvent{Type: "text_delta", Data: event.Delta})
		}
		return false, nil
	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		return false, errors.New("current FAIRY Responses request does not accept tool calls")
	case "response.completed":
		if len(event.Response) == 0 {
			return false, errors.New("completed event missing response")
		}
		var response struct {
			ID     string `json:"id"`
			Output []struct {
				Type    string `json:"type"`
				Content []struct {
					Text     string `json:"text"`
					Refusal  string `json:"refusal"`
				} `json:"content"`
			} `json:"output"`
			Usage responsesUsage `json:"usage"`
		}
		if err := json.Unmarshal(event.Response, &response); err != nil {
			return false, fmt.Errorf("parsing completed response: %w", err)
		}
		for _, item := range response.Output {
			if item.Type == "function_call" {
				return false, errors.New("current FAIRY Responses request does not accept tool calls")
			}
		}
		completedText := extractResponsesOutputText(response.Output)
		if s.output == "" && completedText != "" {
			s.output = completedText
			if onEvent != nil {
				onEvent(StreamEvent{Type: "text_delta", Data: completedText})
			}
		} else if completedText != "" && completedText != s.output {
			return false, errors.New("model completion text diverged from streamed deltas")
		}
		if s.output == "" {
			return false, errors.New("model completed without returning text")
		}
		if onEvent != nil {
			if usage := response.Usage.public(); usage != nil {
				onEvent(StreamEvent{Type: "usage", Usage: usage})
			}
			onEvent(StreamEvent{Type: "completed", Data: strings.TrimSpace(response.ID)})
		}
		return true, nil
	case "response.failed", "response.incomplete", "error":
		message := "model failed to complete the response"
		if event.Error != nil && event.Error.Message != "" {
			message = event.Error.Message
		}
		if onEvent != nil {
			onEvent(StreamEvent{Type: "failed", Data: message})
		}
		return false, errors.New(message)
	default:
		if isIgnorableResponsesEvent(event.Type) {
			return false, nil
		}
		return false, fmt.Errorf("responses SSE event type %q is not supported", event.Type)
	}
}

func extractResponsesOutputText(output []struct {
	Type    string `json:"type"`
	Content []struct {
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}) string {
	var builder strings.Builder
	for _, item := range output {
		for _, part := range item.Content {
			if part.Text != "" {
				builder.WriteString(part.Text)
				continue
			}
			if part.Refusal != "" {
				builder.WriteString(part.Refusal)
			}
		}
	}
	return builder.String()
}

func isIgnorableResponsesEvent(eventType string) bool {
	switch eventType {
	case "response.created",
		"response.queued",
		"response.in_progress",
		"response.output_item.added",
		"response.output_item.done",
		"response.content_part.added",
		"response.content_part.done",
		"response.output_text.done",
		"response.refusal.done",
		"response.reasoning_summary_part.added",
		"response.reasoning_summary_part.done",
		"response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done":
		return true
	default:
		return false
	}
}
