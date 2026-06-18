package openaicompatible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Rinai-R/FAIRY/internal/llm"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

const DefaultTimeout = 120

type Adapter struct {
	defaultProfile llm.Profile
	client         option.HTTPClient
}

type Options struct {
	Profile    llm.Profile
	TimeoutSec int
	Client     option.HTTPClient
}

func NewAdapter(options Options) *Adapter {
	timeout := options.TimeoutSec
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: time.Duration(timeout) * time.Second}
	}
	return &Adapter{
		defaultProfile: options.Profile,
		client:         client,
	}
}

func (a *Adapter) Validate(profile llm.Profile) error {
	resolved, err := a.resolveProfile(profile)
	if err != nil {
		return err
	}
	if resolved.Endpoint == "" {
		return errors.New("llm endpoint 不能为空")
	}
	if _, err := url.ParseRequestURI(resolved.Endpoint); err != nil {
		return fmt.Errorf("llm endpoint 无效: %w", err)
	}
	if resolved.APIKey == "" {
		return errors.New("llm api key 不能为空")
	}
	if resolved.APIKey != strings.TrimSpace(resolved.APIKey) {
		return errors.New("llm api key 不能包含首尾空白")
	}
	if resolved.Model == "" {
		return errors.New("llm model 不能为空")
	}
	if strings.TrimSpace(resolved.ExtraBody) != "" {
		var extra map[string]any
		if err := json.Unmarshal([]byte(resolved.ExtraBody), &extra); err != nil {
			return fmt.Errorf("llm extra_body 不是合法 JSON 对象: %w", err)
		}
	}
	return nil
}

func (a *Adapter) CompleteJSON(ctx context.Context, request llm.Request) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	profile, err := a.resolveProfile(request.Profile)
	if err != nil {
		return "", err
	}
	if err := a.Validate(profile); err != nil {
		return "", err
	}
	params := openai.ChatCompletionNewParams{
		Model:       profile.Model,
		Messages:    buildMessages(request.Messages),
		Temperature: openai.Opt(request.Temperature),
	}
	requestOptions := make([]option.RequestOption, 0, 8)
	hasResponseFormat := false
	if strings.TrimSpace(profile.ExtraBody) != "" {
		var extra map[string]any
		if err := json.Unmarshal([]byte(profile.ExtraBody), &extra); err != nil {
			return "", fmt.Errorf("llm extra_body 不是合法 JSON 对象: %w", err)
		}
		for key, value := range extra {
			if key == "response_format" {
				hasResponseFormat = true
			}
			requestOptions = append(requestOptions, option.WithJSONSet(key, value))
		}
	}
	if !hasResponseFormat {
		requestOptions = append(requestOptions, option.WithJSONSet("response_format", map[string]string{"type": "json_object"}))
	}
	client := a.newClient(profile.Endpoint, profile.APIKey)
	resp, err := client.Chat.Completions.New(ctx, params, requestOptions...)
	if err != nil {
		return "", sanitizeError(fmt.Errorf("调用 llm chat completions 失败: %w", err), profile.APIKey)
	}
	content, err := extractCompletionContent(resp)
	if err != nil {
		return "", err
	}
	return content, nil
}

func extractCompletionContent(resp *openai.ChatCompletion) (string, error) {
	if resp == nil {
		return "", errors.New("llm chat completions 响应为空")
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("llm chat completions 响应缺少 choices")
	}
	message := resp.Choices[0].Message
	rawContent := strings.TrimSpace(message.JSON.Content.Raw())
	if rawContent != "" {
		content, err := extractMessageContentRaw(rawContent)
		if err != nil {
			return "", err
		}
		if content != "" {
			return content, nil
		}
	} else if content := strings.TrimSpace(message.Content); content != "" {
		return content, nil
	}

	if refusal := extractMessageRefusal(message); refusal != "" {
		return "", fmt.Errorf("llm chat completions 响应为拒绝内容: %s", providerSnippet(refusal))
	}
	if messageHasReasoningContent(message) {
		return "", errors.New("llm chat completions 响应只有 reasoning_content，缺少 choices[0].message.content")
	}
	if rawContent != "" {
		return "", errors.New("llm chat completions 响应 choices[0].message.content 为空")
	}
	return "", errors.New("llm chat completions 响应缺少 choices[0].message.content")
}

func extractMessageContentRaw(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return "", nil
	}
	switch raw[0] {
	case '"':
		var content string
		if err := json.Unmarshal([]byte(raw), &content); err != nil {
			return "", fmt.Errorf("解析 llm chat completions message.content 字符串失败: %w", err)
		}
		return strings.TrimSpace(content), nil
	case '[':
		return extractContentPartArray(raw)
	case '{':
		return extractContentObject(raw)
	default:
		return "", fmt.Errorf("llm chat completions 响应 choices[0].message.content 类型不支持: %s", raw)
	}
}

type rawContentPart struct {
	Type       string `json:"type"`
	Text       string `json:"text"`
	OutputText string `json:"output_text"`
	Refusal    string `json:"refusal"`
}

func extractContentPartArray(raw string) (string, error) {
	var parts []rawContentPart
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return "", fmt.Errorf("解析 llm chat completions message.content 数组失败: %w", err)
	}
	var builder strings.Builder
	unsupportedTypes := make([]string, 0)
	for _, part := range parts {
		if refusal := strings.TrimSpace(part.Refusal); refusal != "" {
			return "", fmt.Errorf("llm chat completions 响应为拒绝内容: %s", providerSnippet(refusal))
		}
		partType := strings.TrimSpace(part.Type)
		text := firstNonEmpty(part.Text, part.OutputText)
		switch partType {
		case "", "text", "output_text":
			if text != "" {
				builder.WriteString(text)
			}
		case "refusal":
			return "", errors.New("llm chat completions 响应为拒绝内容")
		default:
			unsupportedTypes = append(unsupportedTypes, partType)
		}
	}
	if builder.Len() > 0 {
		return strings.TrimSpace(builder.String()), nil
	}
	if len(unsupportedTypes) > 0 {
		return "", fmt.Errorf("llm chat completions message.content 数组不支持片段类型: %s", strings.Join(unsupportedTypes, ", "))
	}
	return "", errors.New("llm chat completions message.content 数组不包含 text 片段")
}

func extractContentObject(raw string) (string, error) {
	var part rawContentPart
	if err := json.Unmarshal([]byte(raw), &part); err != nil {
		return "", fmt.Errorf("解析 llm chat completions message.content 对象失败: %w", err)
	}
	if refusal := strings.TrimSpace(part.Refusal); refusal != "" {
		return "", fmt.Errorf("llm chat completions 响应为拒绝内容: %s", providerSnippet(refusal))
	}
	partType := strings.TrimSpace(part.Type)
	text := firstNonEmpty(part.Text, part.OutputText)
	switch partType {
	case "", "text", "output_text":
		if text != "" {
			return text, nil
		}
		if partType == "" {
			return raw, nil
		}
		return "", fmt.Errorf("llm chat completions message.content 对象 type=%q 但缺少 text", partType)
	case "refusal":
		return "", errors.New("llm chat completions 响应为拒绝内容")
	default:
		return "", fmt.Errorf("llm chat completions message.content 对象类型不支持: %s", partType)
	}
}

func extractMessageRefusal(message openai.ChatCompletionMessage) string {
	if refusal := strings.TrimSpace(message.Refusal); refusal != "" {
		return refusal
	}
	return extractJSONString(message.JSON.Refusal.Raw())
}

func messageHasReasoningContent(message openai.ChatCompletionMessage) bool {
	field, ok := message.JSON.ExtraFields["reasoning_content"]
	if !ok {
		return false
	}
	raw := strings.TrimSpace(field.Raw())
	if raw == "" || raw == "null" || raw == `""` {
		return false
	}
	if text := extractJSONString(raw); text != "" {
		return true
	}
	return true
}

func extractJSONString(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return ""
	}
	var value string
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func providerSnippet(text string) string {
	const maxRunes = 120
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes]) + "..."
}

func (a *Adapter) resolveProfile(override llm.Profile) (llm.Profile, error) {
	profile := a.defaultProfile
	if strings.TrimSpace(override.Endpoint) != "" {
		profile.Endpoint = strings.TrimRight(strings.TrimSpace(override.Endpoint), "/")
	}
	if override.APIKey != "" {
		profile.APIKey = override.APIKey
	}
	if strings.TrimSpace(override.Model) != "" {
		profile.Model = strings.TrimSpace(override.Model)
	}
	if strings.TrimSpace(override.ExtraBody) != "" {
		profile.ExtraBody = strings.TrimSpace(override.ExtraBody)
	}
	profile.Endpoint = normalizeBaseURL(profile.Endpoint)
	return profile, nil
}

func (a *Adapter) newClient(baseURL string, apiKey string) openai.Client {
	options := []option.RequestOption{
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
	}
	if a.client != nil {
		options = append(options, option.WithHTTPClient(a.client))
	}
	return openai.NewClient(options...)
}

func normalizeBaseURL(baseURL string) string {
	return strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(baseURL), "/"), "/chat/completions")
}

func buildMessages(messages []llm.Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, message := range messages {
		content := openai.String(strings.TrimSpace(message.Content))
		switch message.Role {
		case "system":
			out = append(out, openai.ChatCompletionMessageParamUnion{
				OfSystem: &openai.ChatCompletionSystemMessageParam{
					Content: openai.ChatCompletionSystemMessageParamContentUnion{
						OfString: content,
					},
				},
			})
		case "assistant":
			out = append(out, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &openai.ChatCompletionAssistantMessageParam{
					Content: openai.ChatCompletionAssistantMessageParamContentUnion{
						OfString: content,
					},
				},
			})
		default:
			out = append(out, openai.ChatCompletionMessageParamUnion{
				OfUser: &openai.ChatCompletionUserMessageParam{
					Content: openai.ChatCompletionUserMessageParamContentUnion{
						OfString: content,
					},
				},
			})
		}
	}
	return out
}

func sanitizeError(err error, secret string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if secret != "" {
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	return errors.New(message)
}
