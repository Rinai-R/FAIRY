package httpvoice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/health"
	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app"
)

type Engine struct {
	Provider     string
	Endpoint     string
	Method       string
	Path         string
	ContentType  string
	BodyTemplate string
	OutputFormat string
	HealthPath   string
	Headers      map[string]string
	OutputDir    string
	BaseURL      string
	Client       *http.Client
}

type Options struct {
	Provider     string
	Endpoint     string
	Method       string
	Path         string
	ContentType  string
	BodyTemplate string
	OutputFormat string
	HealthPath   string
	Headers      map[string]string
	OutputDir    string
	BaseURL      string
	Timeout      time.Duration
}

func NewEngine(options Options) *Engine {
	if options.Method == "" {
		options.Method = http.MethodPost
	}
	if options.Path == "" {
		options.Path = "/v1/synthesize"
	}
	if options.ContentType == "" {
		options.ContentType = "application/json"
	}
	if options.OutputFormat == "" {
		options.OutputFormat = "wav"
	}
	if options.OutputDir == "" {
		options.OutputDir = "tmp/audio"
	}
	if options.BaseURL == "" {
		options.BaseURL = "/audio/"
	}
	if options.Timeout <= 0 {
		options.Timeout = 2 * time.Minute
	}
	return &Engine{
		Provider:     options.Provider,
		Endpoint:     normalizeEndpoint(options.Endpoint),
		Method:       options.Method,
		Path:         options.Path,
		ContentType:  options.ContentType,
		BodyTemplate: options.BodyTemplate,
		OutputFormat: strings.TrimPrefix(options.OutputFormat, "."),
		HealthPath:   options.HealthPath,
		Headers:      options.Headers,
		OutputDir:    options.OutputDir,
		BaseURL:      options.BaseURL,
		Client:       &http.Client{Timeout: options.Timeout},
	}
}

func (e *Engine) Synthesize(ctx context.Context, input voice.Input) (app.AudioResult, error) {
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return app.AudioResult{Format: e.OutputFormat, Placeholder: true}, nil
	}
	endpoint := normalizeEndpoint(first(input.Profile.Endpoint, e.Endpoint))
	if endpoint == "" {
		return app.AudioResult{}, fmt.Errorf("%s endpoint 不能为空", e.Provider)
	}
	body, err := e.renderBody(input)
	if err != nil {
		return app.AudioResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, e.Method, endpointWithPath(endpoint, e.Path), strings.NewReader(body))
	if err != nil {
		return app.AudioResult{}, err
	}
	req.Header.Set("content-type", e.ContentType)
	for key, value := range e.Headers {
		req.Header.Set(key, value)
	}

	resp, err := e.client().Do(req)
	if err != nil {
		return app.AudioResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return app.AudioResult{}, fmt.Errorf("%s voice http 失败: %s: %s%s", e.Provider, resp.Status, strings.TrimSpace(string(msg)), endpointHint(e.Provider, endpoint, resp.StatusCode))
	}
	if isJSONResponse(resp.Header.Get("content-type")) {
		return parseStandardResponse(resp.Body, e.OutputFormat)
	}
	format := formatFromContentType(resp.Header.Get("content-type"), first(input.Profile.MediaType, e.OutputFormat))
	if err := os.MkdirAll(e.OutputDir, 0o755); err != nil {
		return app.AudioResult{}, err
	}
	name := fmt.Sprintf("%d.%s", time.Now().UnixNano(), extension(format))
	path := filepath.Join(e.OutputDir, name)
	file, err := os.Create(path)
	if err != nil {
		return app.AudioResult{}, err
	}
	if _, err := io.Copy(file, resp.Body); err != nil {
		file.Close()
		return app.AudioResult{}, err
	}
	if err := file.Close(); err != nil {
		return app.AudioResult{}, err
	}
	return app.AudioResult{
		URL:         e.BaseURL + name,
		Format:      format,
		DurationMS:  estimateDuration(text),
		Placeholder: false,
	}, nil
}

func (e *Engine) Check(ctx context.Context) health.Result {
	return health.Measure("voice", e.Provider, func(ctx context.Context) (health.Status, string, map[string]string) {
		if e.Endpoint == "" {
			return health.StatusDown, "endpoint 不能为空", nil
		}
		path := e.HealthPath
		if path == "" {
			path = "/"
		}
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, endpointWithPath(e.Endpoint, path), bytes.NewReader(nil))
		if err != nil {
			return health.StatusDown, err.Error(), nil
		}
		for key, value := range e.Headers {
			req.Header.Set(key, value)
		}
		resp, err := e.client().Do(req)
		if err != nil {
			return health.StatusDown, err.Error(), map[string]string{"endpoint": e.Endpoint}
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			return health.StatusOK, "HTTP voice provider 可访问", map[string]string{"endpoint": e.Endpoint, "status": resp.Status}
		}
		return health.StatusDegraded, resp.Status, map[string]string{"endpoint": e.Endpoint}
	})(ctx)
}

func (e *Engine) renderBody(input voice.Input) (string, error) {
	template := e.BodyTemplate
	if template == "" {
		payload := standardSynthesisRequest{
			VoiceID:  input.Profile.VoiceID,
			Text:     strings.TrimSpace(input.Text),
			Language: input.Profile.TextLang,
			Format:   first(input.Profile.MediaType, e.OutputFormat),
			Style:    input.Plan.Style,
			Speed:    input.Plan.Speed,
			Pitch:    input.Plan.Pitch,
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	replacements := map[string]string{
		"{{text}}":     jsonEscape(input.Text),
		"{{voice_id}}": jsonEscape(first(input.Profile.VoiceID, input.Plan.VoiceID, input.Character.VoiceID)),
		"{{style}}":    jsonEscape(input.Plan.Style),
		"{{speed}}":    fmt.Sprintf("%g", input.Plan.Speed),
		"{{pitch}}":    fmt.Sprintf("%g", input.Plan.Pitch),
	}
	for key, value := range replacements {
		template = strings.ReplaceAll(template, key, value)
	}
	return template, nil
}

type standardSynthesisRequest struct {
	VoiceID  string  `json:"voice_id,omitempty"`
	Text     string  `json:"text"`
	Language string  `json:"language,omitempty"`
	Format   string  `json:"format,omitempty"`
	Style    string  `json:"style,omitempty"`
	Speed    float64 `json:"speed,omitempty"`
	Pitch    float64 `json:"pitch,omitempty"`
}

type standardSynthesisResponse struct {
	AudioURL   string `json:"audio_url"`
	Format     string `json:"format,omitempty"`
	DurationMS int    `json:"duration_ms,omitempty"`
}

func parseStandardResponse(body io.Reader, fallbackFormat string) (app.AudioResult, error) {
	var payload standardSynthesisResponse
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&payload); err != nil {
		return app.AudioResult{}, fmt.Errorf("voice service response 必须是 JSON: %w", err)
	}
	if strings.TrimSpace(payload.AudioURL) == "" {
		return app.AudioResult{}, fmt.Errorf("voice service response audio_url 不能为空")
	}
	return app.AudioResult{
		URL:         strings.TrimSpace(payload.AudioURL),
		Format:      first(payload.Format, fallbackFormat),
		DurationMS:  payload.DurationMS,
		Placeholder: false,
	}, nil
}

func isJSONResponse(value string) bool {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	return contentType == "application/json" || contentType == "application/problem+json"
}

func formatFromContentType(value string, fallback string) string {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	switch contentType {
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/ogg", "application/ogg":
		return "ogg"
	case "audio/aac", "audio/aacp":
		return "aac"
	case "audio/wav", "audio/x-wav", "audio/wave", "audio/vnd.wave":
		return "wav"
	default:
		return strings.TrimPrefix(first(fallback, "wav"), ".")
	}
}

func extension(format string) string {
	switch strings.ToLower(format) {
	case "mp3", "ogg", "aac":
		return strings.ToLower(format)
	default:
		return "wav"
	}
}

func normalizeEndpoint(value string) string {
	endpoint := strings.TrimSpace(value)
	if endpoint == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(endpoint), "http://") && !strings.HasPrefix(strings.ToLower(endpoint), "https://") {
		endpoint = "http://" + endpoint
	}
	return strings.TrimRight(endpoint, "/")
}

func endpointWithPath(endpoint string, path string) string {
	endpoint = normalizeEndpoint(endpoint)
	if path == "" || path == "/" {
		return endpoint
	}
	normalizedPath := "/" + strings.TrimLeft(path, "/")
	if strings.HasSuffix(strings.ToLower(endpoint), strings.ToLower(normalizedPath)) {
		return endpoint
	}
	return strings.TrimRight(endpoint, "/") + normalizedPath
}

func endpointHint(provider string, endpoint string, statusCode int) string {
	if provider != "voice-service" || statusCode != http.StatusNotFound || !isLikelyRawLocalTTSEndpoint(endpoint) {
		return ""
	}
	return "；voice-service endpoint 应指向本机 Gateway/标准语音服务，默认 http://127.0.0.1:8791，不是原始模型服务端口 9880"
}

func isLikelyRawLocalTTSEndpoint(endpoint string) bool {
	host := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(endpoint)), "http://"), "https://")
	host = strings.TrimRight(host, "/")
	return strings.HasPrefix(host, "127.0.0.1:9880") || strings.HasPrefix(host, "localhost:9880")
}

func (e *Engine) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return http.DefaultClient
}

func jsonEscape(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return value
}

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func estimateDuration(text string) int {
	n := len([]rune(text))
	if n == 0 {
		return 0
	}
	return 800 + n*90
}
