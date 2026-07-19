package coreclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	DefaultEndpoint = "http://127.0.0.1:8787"
	DefaultTimeout  = 15 * time.Second
	maxRequestBody  = 1 << 20
	maxJSONBody     = 4 << 20
	maxErrorBody    = 64 << 10
)

var (
	clientBearerPattern = regexp.MustCompile(`(?i)(bearer\s+)\S+`)
	clientSecretPattern = regexp.MustCompile(`(?i)(api[_-]?key\s*[:=]\s*|access[_-]?token\s*[:=]\s*|authorization\s*[:=]\s*|credential\s*[:=]\s*|secret\s*[:=]\s*|password\s*[:=]\s*|token\s*[:=]\s*)\S+`)
)

type Options struct {
	Endpoint   string
	Timeout    time.Duration
	Token      string
	HTTPClient *http.Client
}

type Client struct {
	baseURL *url.URL
	timeout time.Duration
	token   string
	http    *http.Client
}

type Error struct {
	Action   string
	Endpoint string
	Status   int
	Message  string
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s %s: HTTP %d: %s", e.Action, e.Endpoint, e.Status, e.Message)
	}
	return fmt.Sprintf("%s %s: %s", e.Action, e.Endpoint, e.Message)
}

func New(options Options) (*Client, error) {
	endpoint := options.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("endpoint must be an absolute http or https URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("endpoint must not contain a path, query, or fragment")
	}
	parsed.Path = ""
	if options.Token != strings.TrimSpace(options.Token) {
		return nil, errors.New("API token must not contain leading or trailing whitespace")
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{baseURL: parsed, timeout: timeout, token: options.Token, http: httpClient}, nil
}

func (c *Client) Endpoint() string { return c.baseURL.String() }

func (c *Client) Status(ctx context.Context) (Status, error) {
	var result Status
	err := c.doJSON(ctx, "read status", http.MethodGet, "/v1/status", nil, &result)
	if err == nil && (result.ConfigRoot == "" || len(result.Bootstrap) == 0 || len(result.WebSearch) == 0 || len(result.SemanticEmbedding) == 0) {
		err = &Error{Action: "read status", Endpoint: c.url("/v1/status"), Message: "response is missing required status fields"}
	}
	return result, err
}

func (c *Client) doJSON(ctx context.Context, action, method, path string, body []byte, out any) error {
	requestCtx, cancel := c.finiteContext(ctx)
	defer cancel()
	if len(body) > maxRequestBody {
		return &Error{Action: action, Endpoint: c.url(path), Message: "request body exceeds 1 MiB"}
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(requestCtx, method, c.url(path), reader)
	if err != nil {
		return &Error{Action: action, Endpoint: c.url(path), Message: redactClientError(err.Error())}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.authorize(req)
	res, err := c.http.Do(req)
	if err != nil {
		return &Error{Action: action, Endpoint: c.url(path), Message: redactClientError(err.Error())}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return c.responseError(action, path, res)
	}
	mediaType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return &Error{Action: action, Endpoint: c.url(path), Status: res.StatusCode, Message: "response content type is not application/json"}
	}
	return decodeBoundedJSON(res.Body, maxJSONBody, out)
}

func (c *Client) doRawJSON(ctx context.Context, action, method, path string, body []byte) (json.RawMessage, error) {
	var result json.RawMessage
	if err := c.doJSON(ctx, action, method, path, body, &result); err != nil {
		return nil, err
	}
	if !isJSONObject(result) {
		return nil, &Error{Action: action, Endpoint: c.url(path), Message: "response must be a JSON object"}
	}
	return result, nil
}

func (c *Client) responseError(action, path string, res *http.Response) error {
	raw, err := readBounded(res.Body, maxErrorBody)
	if err != nil {
		return &Error{Action: action, Endpoint: c.url(path), Status: res.StatusCode, Message: err.Error()}
	}
	var payload struct {
		Error string `json:"error"`
	}
	message := http.StatusText(res.StatusCode)
	if json.Unmarshal(raw, &payload) == nil && payload.Error != "" {
		message = payload.Error
	}
	return &Error{Action: action, Endpoint: c.url(path), Status: res.StatusCode, Message: redactClientError(message)}
}

func (c *Client) finiteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.timeout)
}

func (c *Client) authorize(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func (c *Client) url(path string) string { return c.baseURL.String() + path }

func decodeBoundedJSON(reader io.Reader, limit int64, out any) error {
	raw, err := readBounded(reader, limit)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode JSON response: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("decode JSON response: trailing data")
	}
	return nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("response body exceeds %d bytes", limit)
	}
	return raw, nil
}

func isJSONObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && json.Valid(trimmed)
}

func validateJSONObject(raw []byte) error {
	if len(raw) > maxRequestBody {
		return errors.New("request body exceeds 1 MiB")
	}
	if !isJSONObject(raw) {
		return errors.New("request body must be one valid JSON object")
	}
	return nil
}

func redactClientError(message string) string {
	message = clientBearerPattern.ReplaceAllString(message, "${1}[REDACTED]")
	return clientSecretPattern.ReplaceAllString(message, "${1}[REDACTED]")
}
