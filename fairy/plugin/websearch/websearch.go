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
	PluginID = "fairy.plugin.web-search"
	ToolName = "web_search"
	Engine   = "duck"
	MaxHits  = 5
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
	query, limit, baseURL, err := parseRequest(envelope.Payload)
	if err != nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, err.Error())
	}
	if call == nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeCapabilityDenied, "http.request: not granted")
	}
	if _, err := getHTTP(ctx, call, HealthEndpoint(baseURL)); err != nil {
		return failHost(envelope.Correlation, err)
	}
	searchURL, err := SearchEndpoint(baseURL, query, limit)
	if err != nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeManifestInvalid, err.Error())
	}
	body, err := getHTTP(ctx, call, searchURL)
	if err != nil {
		return failHost(envelope.Correlation, err)
	}
	sources, err := ParseHits(body, limit)
	if err != nil {
		return sdk.Fail(envelope.Correlation, plugin.CodeModuleTrap, "openserp search parse failed")
	}
	payload, err := json.Marshal(struct {
		Tool   string       `json:"tool"`
		Result SearchResult `json:"result"`
		Source string       `json:"source"`
	}{
		Tool:   ToolName,
		Result: SearchResult{Query: query, Sources: sources},
		Source: "plugin",
	})
	if err != nil {
		return plugin.Envelope{}, err
	}
	return sdk.Result(envelope.Correlation, payload)
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

func getHTTP(ctx context.Context, call func(context.Context, string, json.RawMessage) ([]byte, error), target string) ([]byte, error) {
	payload, err := json.Marshal(map[string]string{"method": "GET", "url": target})
	if err != nil {
		return nil, err
	}
	raw, err := call(ctx, "http.request", payload)
	if err != nil {
		return nil, err
	}
	var result hostResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, &plugin.CodedError{Code: plugin.CodeModuleTrap, Message: "http.request result is invalid"}
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
		return nil, &plugin.CodedError{Code: code, Message: message}
	}
	var response httpResponse
	if err := json.Unmarshal(result.Body, &response); err != nil {
		return nil, &plugin.CodedError{Code: plugin.CodeModuleTrap, Message: "http.request body is invalid"}
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, &plugin.CodedError{Code: plugin.CodeModuleTrap, Message: fmt.Sprintf("openserp status %d", response.Status)}
	}
	return []byte(response.Body), nil
}

func failHost(correlation plugin.Correlation, err error) (plugin.Envelope, error) {
	var coded *plugin.CodedError
	if errors.As(err, &coded) {
		return sdk.Fail(correlation, coded.Code, coded.Message)
	}
	return plugin.Envelope{}, err
}
