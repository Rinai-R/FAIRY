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
	output        string
	functionCalls []FunctionCall
}

func (s *responsesStreamState) handle(payload string, onEvent func(StreamEvent)) (done bool, err error) {
	var event struct {
		Type     string          `json:"type"`
		Delta    string          `json:"delta"`
		Response json.RawMessage `json:"response"`
		Item     json.RawMessage `json:"item"`
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
		// Arguments are finalized on response.completed output items.
		return false, nil
	case "response.output_item.done":
		if call, ok := parseResponsesFunctionCallItem(event.Item); ok {
			s.functionCalls = append(s.functionCalls, call)
		}
		return false, nil
	case "response.completed":
		if len(event.Response) == 0 {
			return false, errors.New("completed event missing response")
		}
		var response struct {
			ID     string `json:"id"`
			Output []struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
				Content   []struct {
					Text    string `json:"text"`
					Refusal string `json:"refusal"`
				} `json:"content"`
			} `json:"output"`
			Usage responsesUsage `json:"usage"`
		}
		if err := json.Unmarshal(event.Response, &response); err != nil {
			return false, fmt.Errorf("parsing completed response: %w", err)
		}
		calls := make([]FunctionCall, 0)
		for _, item := range response.Output {
			if item.Type != "function_call" {
				continue
			}
			callID := strings.TrimSpace(item.CallID)
			if callID == "" {
				callID = strings.TrimSpace(item.ID)
			}
			calls = append(calls, FunctionCall{
				CallID:    callID,
				Name:      strings.TrimSpace(item.Name),
				Arguments: item.Arguments,
			})
		}
		if len(calls) == 0 && len(s.functionCalls) > 0 {
			calls = append(calls, s.functionCalls...)
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
		if s.output == "" && len(calls) == 0 {
			return false, errors.New("model completed without returning text")
		}
		if onEvent != nil {
			if len(calls) > 0 {
				onEvent(StreamEvent{Type: "function_calls", FunctionCalls: calls})
			}
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

func parseResponsesFunctionCallItem(raw json.RawMessage) (FunctionCall, bool) {
	if len(raw) == 0 {
		return FunctionCall{}, false
	}
	var item struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &item); err != nil || item.Type != "function_call" {
		return FunctionCall{}, false
	}
	callID := strings.TrimSpace(item.CallID)
	if callID == "" {
		callID = strings.TrimSpace(item.ID)
	}
	if strings.TrimSpace(item.Name) == "" {
		return FunctionCall{}, false
	}
	return FunctionCall{
		CallID:    callID,
		Name:      strings.TrimSpace(item.Name),
		Arguments: item.Arguments,
	}, true
}

func extractResponsesOutputText(output []struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
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
