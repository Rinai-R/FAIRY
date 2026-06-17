package httpvoice

import (
	"bytes"
	"context"
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
		Endpoint:     strings.TrimRight(options.Endpoint, "/"),
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
	if e.Endpoint == "" {
		return app.AudioResult{}, fmt.Errorf("%s endpoint 不能为空", e.Provider)
	}
	body := e.renderBody(input)
	req, err := http.NewRequestWithContext(ctx, e.Method, e.Endpoint+e.Path, strings.NewReader(body))
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
		return app.AudioResult{}, fmt.Errorf("%s voice http 失败: %s: %s", e.Provider, resp.Status, strings.TrimSpace(string(msg)))
	}
	if err := os.MkdirAll(e.OutputDir, 0o755); err != nil {
		return app.AudioResult{}, err
	}
	name := fmt.Sprintf("%d.%s", time.Now().UnixNano(), e.OutputFormat)
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
		Format:      e.OutputFormat,
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
		req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, e.Endpoint+path, bytes.NewReader(nil))
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

func (e *Engine) renderBody(input voice.Input) string {
	template := e.BodyTemplate
	if template == "" {
		template = `{"text":"{{text}}"}`
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
	return template
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
