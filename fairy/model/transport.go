package model

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Usage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
}

type StreamEvent struct {
	Type  string `json:"type"`
	Data  string `json:"data,omitempty"`
	Usage *Usage `json:"usage,omitempty"`
}

type HTTPTransport struct {
	Client *http.Client
}

func (t HTTPTransport) Execute(ctx context.Context, draft RequestDraft, bearerKey string, onEvent func(StreamEvent)) error {
	if ctx == nil {
		return errors.New("model transport context is required")
	}
	if draft.URL == "" {
		return errors.New("model request URL is required")
	}
	if draft.Method == "" {
		return errors.New("model request method is required")
	}
	if draft.BodyJSON == "" {
		return errors.New("model request body is required")
	}
	if draft.AuthRequirement == AuthRequirementBearerKey && bearerKey == "" {
		return errors.New("model bearer credential is required")
	}

	request, err := http.NewRequestWithContext(ctx, draft.Method, draft.URL, strings.NewReader(draft.BodyJSON))
	if err != nil {
		return fmt.Errorf("creating model HTTP request: %w", err)
	}
	contentType := draft.ContentType
	if contentType == "" {
		contentType = "application/json"
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "text/event-stream")
	if draft.AuthRequirement == AuthRequirementBearerKey {
		request.Header.Set("Authorization", "Bearer "+bearerKey)
	}

	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return scrubSecret(err, bearerKey)
		}
		return scrubSecret(fmt.Errorf("executing model HTTP request: %w", err), bearerKey)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = response.Status
		}
		return scrubSecret(fmt.Errorf("model HTTP request failed with status %d: %s", response.StatusCode, message), bearerKey)
	}

	if err := parseSSE(ctx, response.Body, draft.Protocol, onEvent); err != nil {
		return scrubSecret(err, bearerKey)
	}
	return nil
}

func ExecuteRequest(ctx context.Context, draft RequestDraft, bearerKey string, onEvent func(StreamEvent)) error {
	return HTTPTransport{}.Execute(ctx, draft, bearerKey, onEvent)
}

func parseSSE(ctx context.Context, reader io.Reader, protocol Protocol, onEvent func(StreamEvent)) error {
	if ctx == nil {
		return errors.New("model SSE context is required")
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var dataLines []string
	seenEvent := false
	responsesState := &responsesStreamState{}
	completed := false
	handlePayload := func(payload string) error {
		seenEvent = true
		if payload == "[DONE]" {
			// Chat Completions treats [DONE] as terminal. Responses only
			// completes on response.completed; bare [DONE] must not mark done.
			if protocol == ProtocolChatCompletions {
				if onEvent != nil {
					onEvent(StreamEvent{Type: "completed"})
				}
				completed = true
			}
			return nil
		}
		switch protocol {
		case ProtocolResponses:
			done, err := responsesState.handle(payload, onEvent)
			if err != nil {
				return err
			}
			if done {
				completed = true
			}
			return nil
		case ProtocolChatCompletions:
			return parseChatCompletionsPayload(payload, onEvent)
		default:
			return fmt.Errorf("model protocol %q is not supported", protocol)
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		if line == "" {
			if len(dataLines) == 0 {
				continue
			}
			payload := strings.Join(dataLines, "\n")
			dataLines = dataLines[:0]
			if err := handlePayload(payload); err != nil {
				return err
			}
			if completed && protocol == ProtocolResponses {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") || strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			return fmt.Errorf("malformed SSE line %q", line)
		}
		dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("reading model SSE stream: %w", err)
	}
	if len(dataLines) > 0 {
		payload := strings.Join(dataLines, "\n")
		if err := handlePayload(payload); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !seenEvent {
		return errors.New("model SSE stream did not contain any events")
	}
	if protocol == ProtocolResponses && !completed {
		return errors.New("IncompleteStream: model stream ended before response.completed")
	}
	return nil
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (u responsesUsage) public() *Usage {
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return nil
	}
	return &Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens}
}

func parseChatCompletionsPayload(payload string, onEvent func(StreamEvent)) error {
	var event struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(bytes.NewBufferString(payload))
	if err := decoder.Decode(&event); err != nil {
		return fmt.Errorf("parsing chat completions SSE payload: %w", err)
	}
	if event.Error != nil {
		if onEvent != nil {
			onEvent(StreamEvent{Type: "failed", Data: event.Error.Message})
		}
		return nil
	}
	if event.Usage != nil && onEvent != nil {
		onEvent(StreamEvent{Type: "usage", Usage: &Usage{PromptTokens: event.Usage.PromptTokens, CompletionTokens: event.Usage.CompletionTokens}})
	}
	if len(event.Choices) == 0 {
		if event.Usage != nil {
			return nil
		}
		return errors.New("chat completions SSE payload missing choices")
	}
	choice := event.Choices[0]
	if choice.Delta.Content != "" {
		if onEvent != nil {
			onEvent(StreamEvent{Type: "text_delta", Data: choice.Delta.Content})
		}
	}
	if choice.FinishReason != "" && onEvent != nil {
		onEvent(StreamEvent{Type: "completed"})
	}
	return nil
}

func scrubSecret(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	message := strings.ReplaceAll(err.Error(), secret, "[REDACTED]")
	return errors.New(message)
}
