package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"fairy/config"
)

type Protocol string

const (
	ProtocolResponses       Protocol = "responses"
	ProtocolChatCompletions Protocol = "chat_completions"
)

type AuthRequirement string

const (
	AuthRequirementBearerKey AuthRequirement = "bearer_key_required"
	AuthRequirementNone      AuthRequirement = "none"
)

type PromptLane string

const (
	PromptLaneRespond PromptLane = "respond"
	PromptLaneCompact PromptLane = "compact"
	PromptLaneExtract PromptLane = "extract"
)

type PromptItemType string

const (
	PromptItemUserMessage      PromptItemType = "user_message"
	PromptItemAssistantMessage PromptItemType = "assistant_message"
	PromptItemContextData      PromptItemType = "context_data"
)

type Connection struct {
	Protocol            Protocol
	Endpoint            string
	Model               string
	ContextWindowTokens uint64
	AuthMode            string
	Capabilities        config.GatewayCapabilities
}

type ModelRequestShape struct {
	Lane            PromptLane `json:"lane"`
	Model           string     `json:"model"`
	Instructions    string     `json:"instructions"`
	MaxOutputTokens uint32     `json:"maxOutputTokens"`
	PromptCacheKey  string     `json:"promptCacheKey,omitempty"`
}

type PromptItem struct {
	Type    PromptItemType `json:"type"`
	Content string         `json:"content"`
}

type CompiledPromptRequest struct {
	Shape              ModelRequestShape `json:"shape"`
	Input              []PromptItem      `json:"input"`
	PreviousResponseID string            `json:"previousResponseId,omitempty"`
}

type RequestDraft struct {
	Protocol        Protocol        `json:"protocol"`
	Method          string          `json:"method"`
	URL             string          `json:"url"`
	ContentType     string          `json:"contentType"`
	AuthRequirement AuthRequirement `json:"authRequirement"`
	BodyJSON        string          `json:"bodyJSON"`
}

func ConnectionFromStatus(status config.ModelConnectionStatus) (Connection, error) {
	if !status.Configured {
		return Connection{}, errors.New("model connection is not configured")
	}
	protocol, err := parseProtocol(status.Protocol)
	if err != nil {
		return Connection{}, err
	}
	if status.Endpoint == "" {
		return Connection{}, errors.New("model endpoint is required")
	}
	if status.Model == "" {
		return Connection{}, errors.New("model name is required")
	}
	if status.ContextWindowTokens == 0 {
		return Connection{}, errors.New("model context window tokens are required")
	}
	if status.AuthMode != "bearer_key" && status.AuthMode != "no_auth" {
		return Connection{}, fmt.Errorf("model auth mode %q is not supported", status.AuthMode)
	}
	return Connection{
		Protocol:            protocol,
		Endpoint:            status.Endpoint,
		Model:               status.Model,
		ContextWindowTokens: status.ContextWindowTokens,
		AuthMode:            status.AuthMode,
		Capabilities:        status.Capabilities,
	}, nil
}

func ConnectionFromConfig(value config.ModelConnection) (Connection, error) {
	protocol, err := parseProtocol(value.Protocol)
	if err != nil {
		return Connection{}, err
	}
	if value.ConnectionID == "" {
		return Connection{}, errors.New("model connection_id is required")
	}
	if value.Endpoint == "" {
		return Connection{}, errors.New("model endpoint is required")
	}
	if value.Model == "" {
		return Connection{}, errors.New("model name is required")
	}
	if value.ContextWindowTokens == 0 {
		return Connection{}, errors.New("model context window tokens are required")
	}
	if value.AuthMode != "bearer_key" && value.AuthMode != "no_auth" {
		return Connection{}, fmt.Errorf("model auth mode %q is not supported", value.AuthMode)
	}
	return Connection{
		Protocol:            protocol,
		Endpoint:            value.Endpoint,
		Model:               value.Model,
		ContextWindowTokens: value.ContextWindowTokens,
		AuthMode:            value.AuthMode,
		Capabilities:        value.Capabilities,
	}, nil
}

func BuildRequestDraft(connection Connection, request CompiledPromptRequest) (RequestDraft, error) {
	if request.Shape.Model != connection.Model {
		return RequestDraft{}, errors.New("request model does not match configured model")
	}
	if request.Shape.MaxOutputTokens == 0 {
		return RequestDraft{}, errors.New("request max output tokens must be greater than zero")
	}
	if err := validateLane(request.Shape.Lane); err != nil {
		return RequestDraft{}, err
	}
	endpoint, err := protocolURL(connection.Endpoint, connection.Protocol)
	if err != nil {
		return RequestDraft{}, err
	}
	body, err := requestBody(connection, request)
	if err != nil {
		return RequestDraft{}, err
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return RequestDraft{}, fmt.Errorf("serializing model request body: %w", err)
	}

	return RequestDraft{
		Protocol:        connection.Protocol,
		Method:          "POST",
		URL:             endpoint,
		ContentType:     "application/json",
		AuthRequirement: authRequirement(connection.AuthMode),
		BodyJSON:        string(bodyJSON),
	}, nil
}

func parseProtocol(value string) (Protocol, error) {
	switch Protocol(value) {
	case ProtocolResponses:
		return ProtocolResponses, nil
	case ProtocolChatCompletions:
		return ProtocolChatCompletions, nil
	default:
		return "", fmt.Errorf("model protocol %q is not supported", value)
	}
}

func validateLane(lane PromptLane) error {
	switch lane {
	case PromptLaneRespond, PromptLaneCompact, PromptLaneExtract:
		return nil
	default:
		return fmt.Errorf("prompt lane %q is not supported", lane)
	}
}

func authRequirement(authMode string) AuthRequirement {
	if authMode == "bearer_key" {
		return AuthRequirementBearerKey
	}
	return AuthRequirementNone
}

func protocolURL(endpoint string, protocol Protocol) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parsing model endpoint: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("model endpoint must include scheme and host")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("model endpoint must not include query or fragment")
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	last := ""
	if len(segments) > 0 {
		last = segments[len(segments)-1]
	}
	if last == "responses" || (len(segments) >= 2 && segments[len(segments)-2] == "chat" && last == "completions") {
		return "", errors.New("model endpoint must be a base URL, not a protocol resource URL")
	}
	resource := "responses"
	if protocol == ProtocolChatCompletions {
		resource = "chat/completions"
	}
	parsed.Path = "/" + path.Join(strings.Trim(parsed.Path, "/"), resource)
	return parsed.String(), nil
}

func requestBody(connection Connection, request CompiledPromptRequest) (any, error) {
	switch connection.Protocol {
	case ProtocolResponses:
		return responsesBody(connection, request)
	case ProtocolChatCompletions:
		return chatCompletionsBody(connection, request)
	default:
		return nil, fmt.Errorf("model protocol %q is not supported", connection.Protocol)
	}
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func mapPromptItems(items []PromptItem, lane PromptLane) ([]openAIMessage, error) {
	messages := make([]openAIMessage, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case PromptItemUserMessage, PromptItemContextData:
			messages = append(messages, openAIMessage{Role: "user", Content: item.Content})
		case PromptItemAssistantMessage:
			content := item.Content
			if lane == PromptLaneRespond {
				encoded, err := json.Marshal(struct {
					Chains []replyChain `json:"chains"`
				}{
					Chains: []replyChain{{VisualState: "idle", Text: item.Content}},
				})
				if err != nil {
					return nil, fmt.Errorf("serializing assistant reply chain history: %w", err)
				}
				content = string(encoded)
			}
			messages = append(messages, openAIMessage{Role: "assistant", Content: content})
		default:
			return nil, fmt.Errorf("prompt item type %q is not supported", item.Type)
		}
	}
	return messages, nil
}

type replyChain struct {
	VisualState string `json:"visualState"`
	Text        string `json:"text"`
}

type responsesRequestBody struct {
	Model              string          `json:"model"`
	Instructions       string          `json:"instructions"`
	Input              []openAIMessage `json:"input"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	MaxOutputTokens    uint32          `json:"max_output_tokens"`
	Store              bool            `json:"store"`
	Stream             bool            `json:"stream"`
	Text               textConfig      `json:"text"`
	PromptCacheKey     string          `json:"prompt_cache_key,omitempty"`
}

type textConfig struct {
	Format textFormat `json:"format"`
}

type textFormat struct {
	Type string `json:"type"`
}

func responsesBody(connection Connection, request CompiledPromptRequest) (responsesRequestBody, error) {
	input, err := mapPromptItems(request.Input, request.Shape.Lane)
	if err != nil {
		return responsesRequestBody{}, err
	}
	promptCacheKey := ""
	if connection.Capabilities.PromptCacheKey {
		if request.Shape.PromptCacheKey == "" {
			return responsesRequestBody{}, errors.New("responses request requires prompt cache key")
		}
		promptCacheKey = request.Shape.PromptCacheKey
	}
	return responsesRequestBody{
		Model:              connection.Model,
		Instructions:       request.Shape.Instructions,
		Input:              input,
		PreviousResponseID: request.PreviousResponseID,
		MaxOutputTokens:    request.Shape.MaxOutputTokens,
		Store:              false,
		Stream:             true,
		Text:               textConfig{Format: textFormat{Type: "text"}},
		PromptCacheKey:     promptCacheKey,
	}, nil
}

type chatCompletionsRequestBody struct {
	Model          string          `json:"model"`
	Messages       []openAIMessage `json:"messages"`
	Stream         bool            `json:"stream"`
	StreamOptions  streamOptions   `json:"stream_options"`
	MaxTokens      uint32          `json:"max_tokens"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type responseFormat struct {
	Type string `json:"type"`
}

func chatCompletionsBody(connection Connection, request CompiledPromptRequest) (chatCompletionsRequestBody, error) {
	if request.PreviousResponseID != "" {
		return chatCompletionsRequestBody{}, errors.New("chat completions does not support previous response id")
	}
	messages, err := mapPromptItems(request.Input, request.Shape.Lane)
	if err != nil {
		return chatCompletionsRequestBody{}, err
	}
	messages = append([]openAIMessage{{Role: "system", Content: request.Shape.Instructions}}, messages...)
	var format *responseFormat
	if request.Shape.Lane == PromptLaneRespond {
		format = &responseFormat{Type: "json_object"}
	}
	return chatCompletionsRequestBody{
		Model:          connection.Model,
		Messages:       messages,
		Stream:         true,
		StreamOptions:  streamOptions{IncludeUsage: true},
		MaxTokens:      request.Shape.MaxOutputTokens,
		ResponseFormat: format,
	}, nil
}
