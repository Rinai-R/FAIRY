package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"fairy/plugin"
	"fairy/plugin/sdk"
)

const (
	PluginID  = "fairy.plugin.web-search"
	ToolName  = "web_search"
	FetchTool = "web_fetch"
	Engine    = "mega"
	MaxHits   = 5
)

type Source struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Rank    uint8  `json:"rank"`
}

type SearchResult struct {
	Query   string   `json:"query"`
	Sources []Source `json:"sources"`
}

type FetchResult struct {
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
	Body        string `json:"body"`
}

type toolPayload struct {
	Tool      string          `json:"tool"`
	Result    json.RawMessage `json:"result"`
	Source    string          `json:"source"`
	Knowledge json.RawMessage `json:"knowledgeEntries,omitempty"`
}

func Manifest() plugin.Manifest {
	return plugin.Manifest{
		SchemaVersion:       plugin.ManifestSchema,
		ID:                  PluginID,
		Version:             "1.0.0",
		ABI:                 plugin.ABIRange{Min: plugin.ABIVersion, Max: plugin.ABIVersion},
		Entry:               plugin.EntryModule,
		Exports:             plugin.RequiredExports(),
		Capabilities:        []string{"http.request"},
		ConfigSchemaVersion: 1,
		DataSchemaVersion:   1,
	}
}

func NewHandler(call func(context.Context, string, json.RawMessage) ([]byte, error)) func(context.Context, plugin.Envelope) (plugin.Envelope, error) {
	return func(ctx context.Context, envelope plugin.Envelope) (plugin.Envelope, error) {
		return Handle(ctx, envelope, call)
	}
}

func Handle(ctx context.Context, envelope plugin.Envelope, call func(context.Context, string, json.RawMessage) ([]byte, error)) (plugin.Envelope, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return plugin.Envelope{}, &plugin.CodedError{Code: plugin.CodeCancelled, Message: err.Error()}
		}
	}
	if envelope.Kind != "handle" {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, "web-search plugin expects handle envelopes")
	}
	tool, err := peekTool(envelope.Payload)
	if err != nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, err.Error())
	}
	switch tool {
	case ToolName:
		return handleSearch(ctx, envelope, call)
	case FetchTool:
		return handleFetch(ctx, envelope, call)
	default:
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, "web search tool is required")
	}
}

func Discover(instances []plugin.InstanceRecord) (instanceID string, ok bool) {
	for _, instance := range instances {
		if instance.PluginID != PluginID || !instance.Enabled || instance.Lifecycle != "ready" {
			continue
		}
		if !hasHTTPRequest(instance.CapabilityGrants) {
			continue
		}
		return instance.ID, true
	}
	return "", false
}

func DecodeSearchResult(raw json.RawMessage) (SearchResult, error) {
	payload, err := decodeToolPayload(raw, ToolName)
	if err != nil {
		return SearchResult{}, err
	}
	var result SearchResult
	if err := json.Unmarshal(payload.Result, &result); err != nil {
		return SearchResult{}, errors.New("web search result schema is invalid")
	}
	if result.Sources == nil {
		result.Sources = []Source{}
	}
	if len(result.Sources) > MaxHits {
		return SearchResult{}, errors.New("web search result exceeds source bound")
	}
	for _, source := range result.Sources {
		if strings.TrimSpace(source.URL) == "" {
			return SearchResult{}, errors.New("web search result source is missing url")
		}
	}
	return result, nil
}

func DecodeFetchResult(raw json.RawMessage) (FetchResult, error) {
	payload, err := decodeToolPayload(raw, FetchTool)
	if err != nil {
		return FetchResult{}, err
	}
	var result FetchResult
	if err := json.Unmarshal(payload.Result, &result); err != nil {
		return FetchResult{}, errors.New("web fetch result schema is invalid")
	}
	if strings.TrimSpace(result.URL) == "" {
		return FetchResult{}, errors.New("web fetch result is missing url")
	}
	return result, nil
}

func handleSearch(ctx context.Context, envelope plugin.Envelope, call func(context.Context, string, json.RawMessage) ([]byte, error)) (plugin.Envelope, error) {
	query, limit, baseURL, err := parseRequest(envelope.Payload)
	if err != nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, err.Error())
	}
	if call == nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeCapabilityDenied, "http.request: not granted")
	}
	response, err := requestHTTP(ctx, call, HealthEndpoint(baseURL))
	if err != nil {
		return failHost(envelope.Correlation, err)
	}
	if response.Status < 200 || response.Status >= 300 {
		return sdk.Fail(envelope.Correlation, plugin.CodeModuleTrap, fmt.Sprintf("openserp status %d", response.Status))
	}
	searchURL, err := SearchEndpoint(baseURL, query, limit)
	if err != nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, err.Error())
	}
	response, err = requestHTTP(ctx, call, searchURL)
	if err != nil {
		return failHost(envelope.Correlation, err)
	}
	if response.Status < 200 || response.Status >= 300 {
		return sdk.Fail(envelope.Correlation, plugin.CodeModuleTrap, fmt.Sprintf("openserp status %d", response.Status))
	}
	sources, err := ParseHits([]byte(response.Body), limit)
	if err != nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeModuleTrap, "openserp search parse failed")
	}
	return encodeToolResult(envelope.Correlation, ToolName, SearchResult{Query: query, Sources: sources})
}

func handleFetch(ctx context.Context, envelope plugin.Envelope, call func(context.Context, string, json.RawMessage) ([]byte, error)) (plugin.Envelope, error) {
	target, err := parseFetchURL(envelope.Payload)
	if err != nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, err.Error())
	}
	if call == nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeCapabilityDenied, "http.request: not granted")
	}
	response, err := requestHTTP(ctx, call, target)
	if err != nil {
		return failHost(envelope.Correlation, err)
	}
	return encodeToolResult(envelope.Correlation, FetchTool, FetchResult{
		URL:         target,
		Status:      response.Status,
		ContentType: response.ContentType,
		Body:        response.Body,
	})
}

func peekTool(raw json.RawMessage) (string, error) {
	var payload struct {
		Tool string `json:"tool"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", errors.New("web search payload is invalid")
	}
	if payload.Tool == "" {
		return "", errors.New("web search tool is required")
	}
	return payload.Tool, nil
}

func decodeToolPayload(raw json.RawMessage, want string) (toolPayload, error) {
	var payload toolPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return toolPayload{}, errors.New("plugin tool result is invalid")
	}
	if payload.Tool != want || payload.Source != "plugin" {
		return toolPayload{}, errors.New("plugin tool result schema is invalid")
	}
	if len(payload.Knowledge) > 0 && string(payload.Knowledge) != "null" {
		return toolPayload{}, errors.New("plugin must not write knowledge entries")
	}
	if len(payload.Result) == 0 {
		return toolPayload{}, errors.New("plugin tool result is missing")
	}
	return payload, nil
}

func hasHTTPRequest(grants []string) bool {
	for _, grant := range grants {
		if grant == "http.request" {
			return true
		}
	}
	return false
}

func encodeToolResult(correlation plugin.Correlation, tool string, result any) (plugin.Envelope, error) {
	body, err := json.Marshal(result)
	if err != nil {
		return plugin.Envelope{}, err
	}
	payload, err := json.Marshal(toolPayload{Tool: tool, Result: body, Source: "plugin"})
	if err != nil {
		return plugin.Envelope{}, err
	}
	return sdk.Result(correlation, payload)
}

func parseFetchURL(raw json.RawMessage) (string, error) {
	var payload struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Tool != FetchTool {
		return "", errors.New("web fetch payload is invalid")
	}
	var arguments struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(payload.Arguments, &arguments); err != nil {
		return "", errors.New("web fetch arguments are invalid")
	}
	target := strings.TrimSpace(arguments.URL)
	if target == "" {
		return "", errors.New("web fetch url is required")
	}
	return target, nil
}

func parseRequest(raw json.RawMessage) (query string, limit int, baseURL string, err error) {
	var payload struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
		Config    struct {
			BaseURL string `json:"baseURL"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", 0, "", errors.New("web search payload is invalid")
	}
	if payload.Tool != ToolName {
		return "", 0, "", errors.New("web search tool is required")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(payload.Config.BaseURL), "/")
	if baseURL == "" {
		return "", 0, "", errors.New("web search baseURL is required")
	}
	var arguments struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if len(payload.Arguments) > 0 {
		if err := json.Unmarshal(payload.Arguments, &arguments); err != nil {
			return "", 0, "", errors.New("web search arguments are invalid")
		}
	}
	query = strings.TrimSpace(arguments.Query)
	if query == "" {
		return "", 0, "", errors.New("web search query is empty")
	}
	limit = arguments.Limit
	if limit <= 0 || limit > MaxHits {
		limit = MaxHits
	}
	return query, limit, baseURL, nil
}

func HealthEndpoint(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/health"
}

func SearchEndpoint(baseURL, query string, limit int) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", errors.New("web search query is empty")
	}
	if limit <= 0 || limit > MaxHits {
		limit = MaxHits
	}
	return fmt.Sprintf("%s/%s/search?text=%s&limit=%d", strings.TrimRight(baseURL, "/"), Engine, url.QueryEscape(query), limit), nil
}

func ParseHits(body []byte, limit int) ([]Source, error) {
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxHits {
		limit = MaxHits
	}
	sources := make([]Source, 0, limit)
	for _, item := range payload.Results {
		if len(sources) >= limit {
			break
		}
		hitURL := strings.TrimSpace(item.URL)
		if hitURL == "" {
			continue
		}
		sources = append(sources, Source{
			Title:   strings.TrimSpace(item.Title),
			URL:     hitURL,
			Snippet: strings.TrimSpace(item.Snippet),
			Rank:    uint8(len(sources) + 1),
		})
	}
	return sources, nil
}

type hostResult struct {
	OK      bool            `json:"ok"`
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Body    json.RawMessage `json:"body"`
}

type httpResponse struct {
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
	Body        string `json:"body"`
}

func requestHTTP(ctx context.Context, call func(context.Context, string, json.RawMessage) ([]byte, error), target string) (httpResponse, error) {
	payload, err := json.Marshal(map[string]string{"method": "GET", "url": target})
	if err != nil {
		return httpResponse{}, err
	}
	raw, err := call(ctx, "http.request", payload)
	if err != nil {
		return httpResponse{}, err
	}
	var result hostResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return httpResponse{}, &plugin.CodedError{Code: plugin.CodeModuleTrap, Message: "http.request result is invalid"}
	}
	if !result.OK {
		code := result.Code
		if code == "" {
			code = plugin.CodeModuleTrap
		}
		message := result.Message
		if message == "" {
			message = "http.request failed"
		}
		return httpResponse{}, &plugin.CodedError{Code: code, Message: message}
	}
	var response httpResponse
	if err := json.Unmarshal(result.Body, &response); err != nil {
		return httpResponse{}, &plugin.CodedError{Code: plugin.CodeModuleTrap, Message: "http.request body is invalid"}
	}
	return response, nil
}

func failHost(correlation plugin.Correlation, err error) (plugin.Envelope, error) {
	var coded *plugin.CodedError
	if errors.As(err, &coded) {
		return sdk.Fail(correlation, coded.Code, coded.Message)
	}
	return plugin.Envelope{}, err
}
