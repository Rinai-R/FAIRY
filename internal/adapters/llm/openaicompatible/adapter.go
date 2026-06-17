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
	if resp == nil || len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Message.Content) == "" {
		return "", errors.New("llm chat completions 响应缺少 choices[0].message.content")
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
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
