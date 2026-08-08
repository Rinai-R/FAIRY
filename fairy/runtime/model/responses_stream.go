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
	output                strings.Builder
	outputBytes           int
	functionCalls         []FunctionCall
	functionArgumentBytes int
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
		if err := s.appendOutput(event.Delta); err != nil {
			return false, err
		}
		if onEvent != nil {
			onEvent(StreamEvent{Type: "text_delta", Data: event.Delta})
		}
		return false, nil
	case "response.function_call_arguments.delta", "response.function_call_arguments.done":
		// Arguments are finalized on response.completed output items.
		return false, nil
	case "response.output_item.done":
		if call, ok := parseResponsesFunctionCallItem(event.Item); ok {
			if err := appendBoundedFunctionCall(&s.functionCalls, &s.functionArgumentBytes, call); err != nil {
				return false, err
			}
		}
		return false, nil
	case "response.completed":
		if len(event.Response) == 0 {
			return false, errors.New("completed event missing response")
		}
		if len(event.Response) > MaxModelCompletedResponseBytes {
			return false, fmt.Errorf("%w: completed response limit %d bytes", ErrModelStreamCapacity, MaxModelCompletedResponseBytes)
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
		callArgumentBytes := 0
		for _, item := range response.Output {
			if item.Type != "function_call" {
				continue
			}
			callID := strings.TrimSpace(item.CallID)
			if callID == "" {
				callID = strings.TrimSpace(item.ID)
			}
			if err := appendBoundedFunctionCall(&calls, &callArgumentBytes, FunctionCall{
				CallID:    callID,
				Name:      strings.TrimSpace(item.Name),
				Arguments: item.Arguments,
			}); err != nil {
				return false, err
			}
		}
		if len(calls) == 0 && len(s.functionCalls) > 0 {
			calls = append(calls, s.functionCalls...)
		}
		completedText, err := extractResponsesOutputText(response.Output)
		if err != nil {
			return false, err
		}
		streamedText := s.output.String()
		if streamedText == "" && completedText != "" {
			if err := s.appendOutput(completedText); err != nil {
				return false, err
			}
			if onEvent != nil {
				onEvent(StreamEvent{Type: "text_delta", Data: completedText})
			}
		} else if completedText != "" && completedText != streamedText {
			return false, errors.New("model completion text diverged from streamed deltas")
		}
		if s.outputBytes == 0 && len(calls) == 0 {
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

func (s *responsesStreamState) appendOutput(value string) error {
	if len(value) > MaxModelStreamPayloadBytes-s.outputBytes {
		return fmt.Errorf("%w: response text limit %d bytes", ErrModelStreamCapacity, MaxModelStreamPayloadBytes)
	}
	s.output.WriteString(value)
	s.outputBytes += len(value)
	return nil
}

func appendBoundedFunctionCall(calls *[]FunctionCall, argumentBytes *int, call FunctionCall) error {
	if len(*calls) >= MaxModelFunctionCalls {
		return fmt.Errorf("%w: function call limit %d", ErrModelStreamCapacity, MaxModelFunctionCalls)
	}
	if len(call.CallID) > maxModelFunctionIdentifierBytes || len(call.Name) > maxModelFunctionIdentifierBytes {
		return fmt.Errorf("%w: function call identifier limit %d bytes", ErrModelStreamCapacity, maxModelFunctionIdentifierBytes)
	}
	if len(call.Arguments) > MaxModelFunctionArgumentsBytes {
		return fmt.Errorf("%w: function arguments limit %d bytes", ErrModelStreamCapacity, MaxModelFunctionArgumentsBytes)
	}
	if len(call.Arguments) > MaxModelStreamPayloadBytes-*argumentBytes {
		return fmt.Errorf("%w: function arguments total limit %d bytes", ErrModelStreamCapacity, MaxModelStreamPayloadBytes)
	}
	*calls = append(*calls, call)
	*argumentBytes += len(call.Arguments)
	return nil
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
}) (string, error) {
	var builder strings.Builder
	for _, item := range output {
		for _, part := range item.Content {
			if part.Text != "" {
				if len(part.Text) > MaxModelStreamPayloadBytes-builder.Len() {
					return "", fmt.Errorf("%w: response text limit %d bytes", ErrModelStreamCapacity, MaxModelStreamPayloadBytes)
				}
				builder.WriteString(part.Text)
				continue
			}
			if part.Refusal != "" {
				if len(part.Refusal) > MaxModelStreamPayloadBytes-builder.Len() {
					return "", fmt.Errorf("%w: response text limit %d bytes", ErrModelStreamCapacity, MaxModelStreamPayloadBytes)
				}
				builder.WriteString(part.Refusal)
			}
		}
	}
	return builder.String(), nil
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
