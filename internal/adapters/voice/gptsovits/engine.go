package gptsovits

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
	RefAudioPath    string
	PromptText      string
	TextLang        string
	PromptLang      string
	MediaType       string
	TextSplitMethod string
	OutputDir       string
	BaseURL         string
	Client          *http.Client
}

type Options struct {
	Endpoint        string
	RefAudioPath    string
	PromptText      string
	TextLang        string
	PromptLang      string
	MediaType       string
	TextSplitMethod string
	OutputDir       string
	BaseURL         string
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
		Endpoint:        normalizeEndpoint(options.Endpoint),
		RefAudioPath:    strings.TrimSpace(options.RefAudioPath),
		PromptText:      strings.TrimSpace(options.PromptText),
		TextLang:        strings.TrimSpace(options.TextLang),
		PromptLang:      strings.TrimSpace(options.PromptLang),
		MediaType:       strings.TrimSpace(options.MediaType),
		TextSplitMethod: strings.TrimSpace(options.TextSplitMethod),
		OutputDir:       options.OutputDir,
		BaseURL:         options.BaseURL,
		Client:          &http.Client{Timeout: options.Timeout},
	}
}

func (e *Engine) Synthesize(ctx context.Context, input voice.Input) (app.AudioResult, error) {
	text := strings.TrimSpace(input.Text)
	if text == "" {
		return app.AudioResult{Format: DefaultMediaType, Placeholder: true}, nil
	}

	profile := input.Profile
	refAudioPath := first(profile.RefAudioPath, e.RefAudioPath)
	if refAudioPath == "" {
		return app.AudioResult{}, fmt.Errorf("gpt-sovits ref_audio_path 不能为空，请先上传参考音频")
	}
	refAudioPath = normalizeReferenceAudioPath(refAudioPath)
	textLang := first(profile.TextLang, e.TextLang, DefaultTextLang)
	promptLang := first(profile.PromptLang, e.PromptLang, textLang, DefaultPromptLang)
	mediaType := first(profile.MediaType, e.MediaType, DefaultMediaType)
	textSplitMethod := first(profile.TextSplitMethod, e.TextSplitMethod, DefaultTextSplitMethod)

	payload := map[string]any{
		"text":              text,
		"text_lang":         textLang,
		"ref_audio_path":    refAudioPath,
		"prompt_lang":       promptLang,
		"prompt_text":       first(profile.PromptText, e.PromptText),
		"text_split_method": textSplitMethod,
		"batch_size":        1,
		"media_type":        mediaType,
		"streaming_mode":    false,
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return app.AudioResult{}, err
	}
	endpoint := ttsEndpoint(first(profile.Endpoint, e.Endpoint))
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

	format := formatFromContentType(resp.Header.Get("content-type"), mediaType)
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

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeEndpoint(value string) string {
	endpoint := strings.TrimSpace(value)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if !strings.HasPrefix(strings.ToLower(endpoint), "http://") && !strings.HasPrefix(strings.ToLower(endpoint), "https://") {
		endpoint = "http://" + endpoint
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if strings.HasSuffix(strings.ToLower(endpoint), "/tts") {
		endpoint = strings.TrimRight(endpoint[:len(endpoint)-len("/tts")], "/")
	}
	return endpoint
}

func ttsEndpoint(value string) string {
	return normalizeEndpoint(value) + "/tts"
}

func normalizeReferenceAudioPath(value string) string {
	path := strings.TrimSpace(value)
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if _, err := os.Stat(absolute); err == nil {
		return absolute
	}
	return path
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
		return first(fallback, DefaultMediaType)
	}
}

func extension(format string) string {
	switch strings.ToLower(format) {
	case "mp3", "ogg", "aac", "raw":
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
