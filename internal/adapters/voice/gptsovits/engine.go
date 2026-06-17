package gptsovits

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

const (
	DefaultEndpoint        = "http://127.0.0.1:9880"
	DefaultOutputDir       = "tmp/audio"
	DefaultBaseURL         = "/audio/"
	DefaultTextLang        = "zh"
	DefaultPromptLang      = "zh"
	DefaultMediaType       = "wav"
	DefaultTextSplitMethod = "cut5"
)

type Engine struct {
	Endpoint        string
	OutputDir       string
	BaseURL         string
	RefAudioPath    string
	PromptText      string
	TextLang        string
	PromptLang      string
	MediaType       string
	TextSplitMethod string
	Client          *http.Client
}

type Options struct {
	Endpoint        string
	OutputDir       string
	BaseURL         string
	RefAudioPath    string
	PromptText      string
	TextLang        string
	PromptLang      string
	MediaType       string
	TextSplitMethod string
	Timeout         time.Duration
}

func NewEngine(options Options) *Engine {
	if options.Endpoint == "" {
		options.Endpoint = DefaultEndpoint
	}
	if options.OutputDir == "" {
		options.OutputDir = DefaultOutputDir
	}
	if options.BaseURL == "" {
		options.BaseURL = DefaultBaseURL
	}
	if options.TextLang == "" {
		options.TextLang = DefaultTextLang
	}
	if options.PromptLang == "" {
		options.PromptLang = DefaultPromptLang
	}
	if options.MediaType == "" {
		options.MediaType = DefaultMediaType
	}
	if options.TextSplitMethod == "" {
		options.TextSplitMethod = DefaultTextSplitMethod
	}
	if options.Timeout <= 0 {
		options.Timeout = 2 * time.Minute
	}
	return &Engine{
		Endpoint:        strings.TrimRight(options.Endpoint, "/"),
		OutputDir:       options.OutputDir,
		BaseURL:         options.BaseURL,
		RefAudioPath:    options.RefAudioPath,
		PromptText:      options.PromptText,
		TextLang:        options.TextLang,
		PromptLang:      options.PromptLang,
		MediaType:       options.MediaType,
		TextSplitMethod: options.TextSplitMethod,
		Client:          &http.Client{Timeout: options.Timeout},
	}
}

func (e *Engine) Synthesize(ctx context.Context, input voice.Input) (app.AudioResult, error) {
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return app.AudioResult{Format: e.mediaType(input.Profile), Placeholder: true}, nil
	}
	refAudioPath := first(input.Profile.RefAudioPath, e.RefAudioPath)
	if refAudioPath == "" {
		return app.AudioResult{}, errors.New("gpt-sovits ref_audio_path 不能为空")
	}

	payload := map[string]any{
		"text":              text,
		"text_lang":         first(input.Profile.TextLang, e.TextLang),
		"ref_audio_path":    refAudioPath,
		"prompt_lang":       first(input.Profile.PromptLang, e.PromptLang),
		"prompt_text":       first(input.Profile.PromptText, e.PromptText),
		"text_split_method": first(input.Profile.TextSplitMethod, e.TextSplitMethod),
		"batch_size":        1,
		"media_type":        e.mediaType(input.Profile),
		"streaming_mode":    false,
	}
	for key, value := range input.Profile.Extra {
		payload[key] = value
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return app.AudioResult{}, err
	}
	endpoint := strings.TrimRight(first(input.Profile.Endpoint, e.Endpoint), "/") + "/tts"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return app.AudioResult{}, err
	}
	req.Header.Set("content-type", "application/json")

	client := e.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return app.AudioResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return app.AudioResult{}, fmt.Errorf("gpt-sovits tts 失败: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}

	format := e.mediaType(input.Profile)
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
	return health.Measure("voice", string(voice.ProviderGPTSoVITS), func(ctx context.Context) (health.Status, string, map[string]string) {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, e.Endpoint, nil)
		if err != nil {
			return health.StatusDown, err.Error(), nil
		}
		resp, err := e.client().Do(req)
		if err != nil {
			return health.StatusDown, err.Error(), map[string]string{"endpoint": e.Endpoint}
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 500 {
			return health.StatusOK, "GPT-SoVITS HTTP 服务可访问", map[string]string{"endpoint": e.Endpoint, "status": resp.Status}
		}
		return health.StatusDegraded, resp.Status, map[string]string{"endpoint": e.Endpoint}
	})(ctx)
}

func (e *Engine) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return http.DefaultClient
}

func (e *Engine) mediaType(profile app.VoiceProfile) string {
	value := first(profile.MediaType, e.MediaType)
	if value == "" {
		return DefaultMediaType
	}
	return strings.TrimPrefix(value, ".")
}

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func extension(format string) string {
	switch strings.ToLower(format) {
	case "ogg", "aac", "raw":
		return strings.ToLower(format)
	default:
		return "wav"
	}
}

func estimateDuration(text string) int {
	n := len([]rune(text))
	if n == 0 {
		return 0
	}
	return 800 + n*90
}
