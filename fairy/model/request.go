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

type Connection struct {
	Protocol            Protocol
	Endpoint            string
	Model               string
	ContextWindowTokens uint64
	AuthMode            string
	Capabilities        config.GatewayCapabilities
}

type CompiledPromptRequest struct {
	Shape              ModelRequestShape `json:"shape"`
	Input              []PromptItem      `json:"input"`
	PreviousResponseID string            `json:"previousResponseId,omitempty"`
	Tools              []ToolSpec        `json:"tools,omitempty"`
	// CacheInput is local metadata used to derive a stable provider key. It is
	// intentionally excluded from the wire representation.
	CacheInput *CacheKeyInput `json:"-"`
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
	if request.CacheInput != nil {
		if request.CacheInput.Lane != request.Shape.Lane {
			return RequestDraft{}, errors.New("cache key lane does not match request lane")
		}
		if request.CacheInput.Model != request.Shape.Model {
			return RequestDraft{}, errors.New("cache key model does not match request model")
		}
	}
	if err := validatePromptItems(connection, request.Input); err != nil {
		return RequestDraft{}, err
	}
	if request.PreviousResponseID != "" && promptItemsContainToolResult(request.Input) {
		return RequestDraft{}, errors.New("tool result requires a full request rebuild")
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
	case PromptLaneRespond, PromptLaneParticipate, PromptLaneCompact, PromptLaneExtract, PromptLaneSocialLearn, PromptLaneSocialFeedback, PromptLaneKnowledgeReconcile:
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
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type replyChain struct {
	VisualState string `json:"visualState"`
	Text        string `json:"text"`
}

type responsesRequestBody struct {
	Model              string           `json:"model"`
	Instructions       string           `json:"instructions"`
	Input              []any            `json:"input"`
	PreviousResponseID string           `json:"previous_response_id,omitempty"`
	MaxOutputTokens    uint32           `json:"max_output_tokens"`
	Store              bool             `json:"store"`
	Stream             bool             `json:"stream"`
	Text               textConfig       `json:"text"`
	PromptCacheKey     string           `json:"prompt_cache_key,omitempty"`
	Tools              []map[string]any `json:"tools,omitempty"`
}

type textConfig struct {
	Format textFormat `json:"format"`
}

type textFormat struct {
	Type string `json:"type"`
}

func responsesBody(connection Connection, request CompiledPromptRequest) (responsesRequestBody, error) {
	input, err := mapResponsesPromptItems(request.Input, request.Shape.Lane)
	if err != nil {
		return responsesRequestBody{}, err
	}
	promptCacheKey, err := promptCacheKeyFor(connection, request)
	if err != nil {
		return responsesRequestBody{}, err
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
		Tools:              responsesToolDefinitions(request.Tools),
	}, nil
}

func promptCacheKeyFor(connection Connection, request CompiledPromptRequest) (string, error) {
	if !connection.Capabilities.PromptCacheKey {
		return "", nil
	}
	if request.CacheInput != nil {
		key, err := BuildPromptCacheKey(*request.CacheInput)
		if err != nil {
			return "", fmt.Errorf("building prompt cache key: %w", err)
		}
		return key, nil
	}
	if request.Shape.PromptCacheKey == "" {
		return "", errors.New("responses request requires prompt cache key")
	}
	return request.Shape.PromptCacheKey, nil
}

type chatCompletionsRequestBody struct {
	Model          string           `json:"model"`
	Messages       []openAIMessage  `json:"messages"`
	Stream         bool             `json:"stream"`
	StreamOptions  streamOptions    `json:"stream_options"`
	MaxTokens      uint32           `json:"max_tokens"`
	ResponseFormat *responseFormat  `json:"response_format,omitempty"`
	Tools          []map[string]any `json:"tools,omitempty"`
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
	messages, err := mapChatPromptItems(request.Input, request.Shape.Lane)
	if err != nil {
		return chatCompletionsRequestBody{}, err
	}
	messages = append([]openAIMessage{{Role: "system", Content: request.Shape.Instructions}}, messages...)
	var format *responseFormat
	// json_object conflicts with tool calling on many providers; only force it when no tools.
	if (request.Shape.Lane == PromptLaneRespond || request.Shape.Lane == PromptLaneParticipate || request.Shape.Lane == PromptLaneSocialLearn || request.Shape.Lane == PromptLaneSocialFeedback) && len(request.Tools) == 0 {
		format = &responseFormat{Type: "json_object"}
	}
	return chatCompletionsRequestBody{
		Model:          connection.Model,
		Messages:       messages,
		Stream:         true,
		StreamOptions:  streamOptions{IncludeUsage: true},
		MaxTokens:      request.Shape.MaxOutputTokens,
		ResponseFormat: format,
		Tools:          chatToolDefinitions(request.Tools),
	}, nil
}
