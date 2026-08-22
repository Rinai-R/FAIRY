package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
)

// SDKTransport uses the official openai-go client for HTTP + SSE decoding.
type SDKTransport struct {
	HTTPClient     *http.Client
	endpointStrict bool
	endpointClient endpointProviderClientFactory
}

var _ Transport = SDKTransport{}

func (t SDKTransport) Execute(ctx context.Context, draft RequestDraft, bearerKey string, onEvent func(StreamEvent)) error {
	if ctx == nil {
		return errors.New("model transport context is required")
	}
	if draft.URL == "" {
		return errors.New("model request URL is required")
	}
	if draft.BodyJSON == "" {
		return errors.New("model request body is required")
	}
	if draft.AuthRequirement == AuthRequirementBearerKey && bearerKey == "" {
		return errors.New("model bearer credential is required")
	}

	baseURL, err := baseURLFromRequestURL(draft.URL, draft.Protocol)
	if err != nil {
		return err
	}

	opts := []option.RequestOption{
		option.WithBaseURL(baseURL),
	}
	if draft.AuthRequirement == AuthRequirementBearerKey {
		opts = append(opts, option.WithAPIKey(bearerKey))
	} else {
		// Compatible local/test servers may not want Authorization at all.
		opts = append(opts, option.WithAPIKey(""))
	}
	httpClient := t.HTTPClient
	if t.endpointStrict {
		if httpClient != nil {
			return errors.New("endpoint-strict model transport does not accept an injected HTTP client")
		}
		factory := t.endpointClient
		if factory == nil {
			factory = endpointProviderClient
		}
		httpClient, err = factory(baseURL, 5*time.Minute)
		if err != nil {
			return err
		}
	}
	if httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	client := openai.NewClient(opts...)

	switch draft.Protocol {
	case ProtocolResponses:
		err = t.executeResponses(ctx, client, draft.BodyJSON, onEvent)
	case ProtocolChatCompletions:
		err = t.executeChatCompletions(ctx, client, draft.BodyJSON, onEvent)
	default:
		err = fmt.Errorf("model protocol %q is not supported", draft.Protocol)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return context.Canceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		scrubbed := scrubProviderRequestError(err, bearerKey, draft.BodyJSON)
		if errors.Is(err, ErrModelStreamCapacity) {
			detail := strings.TrimPrefix(scrubbed.Error(), ErrModelStreamCapacity.Error())
			detail = strings.TrimPrefix(detail, ": ")
			if detail == "" {
				return ErrModelStreamCapacity
			}
			return fmt.Errorf("%w: %s", ErrModelStreamCapacity, detail)
		}
		return scrubbed
	}
	return nil
}

func (t SDKTransport) executeResponses(ctx context.Context, client openai.Client, bodyJSON string, onEvent func(StreamEvent)) error {
	params := param.Override[responses.ResponseNewParams](json.RawMessage(bodyJSON))
	stream := client.Responses.NewStreaming(ctx, params)
	defer stream.Close()

	state := &responsesStreamState{}
	completed := false
	for stream.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		event := stream.Current()
		done, err := mapResponsesSDKEvent(event, state, onEvent)
		if err != nil {
			return err
		}
		if done {
			completed = true
			break
		}
	}
	if err := stream.Err(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("reading model responses stream: %w", err)
	}
	if !completed {
		return errors.New("IncompleteStream: model stream ended before response.completed")
	}
	return nil
}

func (t SDKTransport) executeChatCompletions(ctx context.Context, client openai.Client, bodyJSON string, onEvent func(StreamEvent)) error {
	params := param.Override[openai.ChatCompletionNewParams](json.RawMessage(bodyJSON))
	stream := client.Chat.Completions.NewStreaming(ctx, params)
	defer stream.Close()

	type pendingToolCall struct {
		ID        string
		Name      string
		Arguments strings.Builder
	}
	pending := map[int64]*pendingToolCall{}
	pendingArgumentBytes := 0
	completed := false
	for stream.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := stream.Current()
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 || chunk.Usage.TotalTokens > 0 {
			if onEvent != nil {
				usage := chatUsageFromRawJSON(chunk.RawJSON())
				usage.PromptTokens = int(chunk.Usage.PromptTokens)
				usage.CompletionTokens = int(chunk.Usage.CompletionTokens)
				onEvent(StreamEvent{
					Type:  "usage",
					Usage: usage,
				})
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.Delta.Content != "" && onEvent != nil {
			onEvent(StreamEvent{Type: "text_delta", Data: choice.Delta.Content})
		}
		for _, toolCall := range choice.Delta.ToolCalls {
			index := toolCall.Index
			slot, ok := pending[index]
			if !ok {
				if len(pending) >= MaxModelFunctionCalls {
					return fmt.Errorf("%w: function call limit %d", ErrModelStreamCapacity, MaxModelFunctionCalls)
				}
				slot = &pendingToolCall{}
				pending[index] = slot
			}
			if toolCall.ID != "" {
				if len(toolCall.ID) > maxModelFunctionIdentifierBytes {
					return fmt.Errorf("%w: function call identifier limit %d bytes", ErrModelStreamCapacity, maxModelFunctionIdentifierBytes)
				}
				slot.ID = toolCall.ID
			}
			if toolCall.Function.Name != "" {
				if len(toolCall.Function.Name) > maxModelFunctionIdentifierBytes {
					return fmt.Errorf("%w: function call identifier limit %d bytes", ErrModelStreamCapacity, maxModelFunctionIdentifierBytes)
				}
				slot.Name = toolCall.Function.Name
			}
			if toolCall.Function.Arguments != "" {
				arguments := toolCall.Function.Arguments
				if len(arguments) > MaxModelFunctionArgumentsBytes-slot.Arguments.Len() {
					return fmt.Errorf("%w: function arguments limit %d bytes", ErrModelStreamCapacity, MaxModelFunctionArgumentsBytes)
				}
				if len(arguments) > MaxModelStreamPayloadBytes-pendingArgumentBytes {
					return fmt.Errorf("%w: function arguments total limit %d bytes", ErrModelStreamCapacity, MaxModelStreamPayloadBytes)
				}
				slot.Arguments.WriteString(toolCall.Function.Arguments)
				pendingArgumentBytes += len(arguments)
			}
		}
		if choice.FinishReason != "" {
			if onEvent != nil {
				if len(pending) > 0 {
					indexes := make([]int64, 0, len(pending))
					for index := range pending {
						indexes = append(indexes, index)
					}
					sort.Slice(indexes, func(i, j int) bool { return indexes[i] < indexes[j] })
					calls := make([]FunctionCall, 0, len(indexes))
					for _, index := range indexes {
						slot := pending[index]
						calls = append(calls, FunctionCall{
							CallID:    strings.TrimSpace(slot.ID),
							Name:      strings.TrimSpace(slot.Name),
							Arguments: slot.Arguments.String(),
						})
					}
					onEvent(StreamEvent{Type: "function_calls", FunctionCalls: calls})
				}
				onEvent(StreamEvent{Type: "completed", FinishReason: string(choice.FinishReason)})
			}
			completed = true
			break
		}
	}
	if err := stream.Err(); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("reading model chat completions stream: %w", err)
	}
	if !completed {
		// Stream may end on data: [DONE] with no finish_reason chunk (or only [DONE]).
		if onEvent != nil {
			onEvent(StreamEvent{Type: "completed"})
		}
	}
	return nil
}

func chatUsageFromRawJSON(raw string) *Usage {
	if raw == "" {
		return &Usage{}
	}
	var chunk struct {
		Usage responsesUsage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
		return &Usage{}
	}
	usage := chunk.Usage.public()
	if usage == nil {
		return &Usage{}
	}
	return usage
}

func mapResponsesSDKEvent(event responses.ResponseStreamEventUnion, state *responsesStreamState, onEvent func(StreamEvent)) (bool, error) {
	switch variant := event.AsAny().(type) {
	case responses.ResponseTextDeltaEvent:
		if variant.Delta == "" {
			return false, nil
		}
		if err := state.appendOutput(variant.Delta); err != nil {
			return false, err
		}
		if onEvent != nil {
			onEvent(StreamEvent{Type: "text_delta", Data: variant.Delta})
		}
		return false, nil
	case responses.ResponseRefusalDeltaEvent:
		if variant.Delta == "" {
			return false, nil
		}
		if err := state.appendOutput(variant.Delta); err != nil {
			return false, err
		}
		if onEvent != nil {
			onEvent(StreamEvent{Type: "text_delta", Data: variant.Delta})
		}
		return false, nil
	case responses.ResponseFunctionCallArgumentsDeltaEvent, responses.ResponseFunctionCallArgumentsDoneEvent:
		return false, nil
	case responses.ResponseCompletedEvent:
		raw := variant.Response.RawJSON()
		if raw == "" {
			return false, errors.New("completed event missing response")
		}
		if len(raw) > MaxModelCompletedResponseBytes {
			return false, fmt.Errorf("%w: completed response limit %d bytes", ErrModelStreamCapacity, MaxModelCompletedResponseBytes)
		}
		return state.handle(`{"type":"response.completed","response":`+raw+`}`, onEvent)
	case responses.ResponseFailedEvent:
		message := "model failed to complete the response"
		if variant.Response.Error.Message != "" {
			message = variant.Response.Error.Message
		}
		if onEvent != nil {
			onEvent(StreamEvent{Type: "failed", Data: message})
		}
		return false, errors.New(message)
	case responses.ResponseIncompleteEvent:
		message := "model failed to complete the response"
		if onEvent != nil {
			onEvent(StreamEvent{Type: "failed", Data: message})
		}
		return false, errors.New(message)
	case responses.ResponseErrorEvent:
		message := variant.Message
		if message == "" {
			message = "model failed to complete the response"
		}
		if onEvent != nil {
			onEvent(StreamEvent{Type: "failed", Data: message})
		}
		return false, errors.New(message)
	default:
		if event.Type == "" || isIgnorableResponsesEvent(event.Type) {
			return false, nil
		}
		// Fall back to raw JSON handling for provider extensions the SDK union omits.
		if raw := event.RawJSON(); raw != "" {
			return state.handle(raw, onEvent)
		}
		return false, fmt.Errorf("responses SSE event type %q is not supported", event.Type)
	}
}

func baseURLFromRequestURL(requestURL string, protocol Protocol) (string, error) {
	parsed, err := url.Parse(requestURL)
	if err != nil {
		return "", fmt.Errorf("parsing model request URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("model request URL must include scheme and host")
	}
	trimmed := strings.TrimSuffix(parsed.Path, "/")
	switch protocol {
	case ProtocolResponses:
		trimmed = strings.TrimSuffix(trimmed, "/responses")
	case ProtocolChatCompletions:
		trimmed = strings.TrimSuffix(trimmed, "/chat/completions")
	}
	parsed.Path = trimmed
	parsed.RawQuery = ""
	parsed.Fragment = ""
	base := parsed.String()
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base, nil
}
