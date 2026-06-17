package comfyui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/health"
	"github.com/Rinai-R/FAIRY/internal/adapters/image"
	"github.com/Rinai-R/FAIRY/internal/app"
)

const (
	DefaultEndpoint  = "http://127.0.0.1:8188"
	DefaultOutputDir = "tmp/images"
	DefaultBaseURL   = "/images/"
)

type Engine struct {
	Endpoint  string
	OutputDir string
	BaseURL   string
	ClientID  string
	Client    *http.Client
}

type Options struct {
	Endpoint  string
	OutputDir string
	BaseURL   string
	ClientID  string
	Timeout   time.Duration
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
	if options.ClientID == "" {
		options.ClientID = "fairy"
	}
	if options.Timeout <= 0 {
		options.Timeout = 5 * time.Minute
	}
	return &Engine{
		Endpoint:  strings.TrimRight(options.Endpoint, "/"),
		OutputDir: options.OutputDir,
		BaseURL:   options.BaseURL,
		ClientID:  options.ClientID,
		Client:    &http.Client{Timeout: options.Timeout},
	}
}

func (e *Engine) Generate(ctx context.Context, input image.Input) (app.ImageResult, error) {
	if len(input.Request.Workflow) == 0 {
		return app.ImageResult{}, errors.New("comfyui workflow 不能为空")
	}
	workflow, err := renderWorkflow(input)
	if err != nil {
		return app.ImageResult{}, err
	}
	endpoint := strings.TrimRight(first(input.Request.Endpoint, e.Endpoint), "/")
	promptID, err := e.queue(ctx, endpoint, workflow)
	if err != nil {
		return app.ImageResult{}, err
	}
	imageRef, err := e.waitImage(ctx, endpoint, promptID)
	if err != nil {
		return app.ImageResult{}, err
	}
	name, err := e.download(ctx, endpoint, imageRef)
	if err != nil {
		return app.ImageResult{}, err
	}
	return app.ImageResult{
		URL:         e.BaseURL + name,
		Format:      extension(imageRef.Filename),
		Prompt:      imagePrompt(input),
		Placeholder: false,
	}, nil
}

func renderWorkflow(input image.Input) (json.RawMessage, error) {
	raw := string(input.Request.Workflow)
	values := workflowValues(input)
	for key, value := range values {
		raw = strings.ReplaceAll(raw, "{{"+key+"}}", escapeJSONString(value))
	}
	out := json.RawMessage(raw)
	if !json.Valid(out) {
		return nil, errors.New("comfyui workflow 渲染后不是合法 JSON")
	}
	return out, nil
}

func workflowValues(input image.Input) map[string]string {
	extra := input.Request.Extra
	values := map[string]string{
		"prompt":              imagePrompt(input),
		"negative_prompt":     input.Request.NegativePrompt,
		"style":               input.Request.Style,
		"size":                input.Request.Size,
		"reference_image_url": first(input.Request.ReferenceImageURL, input.Character.Assets.ReferenceImageURL, input.Character.Assets.PortraitURL, input.Character.AvatarURL),
		"character_id":        input.Character.ID,
		"character_name":      input.Character.DisplayName,
		"emotion":             input.Turn.User.Mode,
		"mood":                "",
		"expression":          "",
	}
	if extra != nil {
		for key, value := range extra {
			values[key] = value
		}
	}
	if values["mood"] == "" {
		values["mood"] = input.Turn.User.Mode
	}
	return values
}

func imagePrompt(input image.Input) string {
	prompt := input.Request.Prompt
	if prompt == "" {
		prompt = input.Turn.Scene.Title
	}
	if input.Character.Assets.StylePrompt != "" && !strings.Contains(prompt, input.Character.Assets.StylePrompt) {
		prompt += ", " + input.Character.Assets.StylePrompt
	}
	return prompt
}

func escapeJSONString(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	quoted := string(raw)
	return strings.TrimSuffix(strings.TrimPrefix(quoted, `"`), `"`)
}

func (e *Engine) Check(ctx context.Context) health.Result {
	return health.Measure("image", string(image.ProviderComfyUI), func(ctx context.Context) (health.Status, string, map[string]string) {
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, e.Endpoint+"/system_stats", nil)
		if err != nil {
			return health.StatusDown, err.Error(), nil
		}
		resp, err := e.client().Do(req)
		if err != nil {
			return health.StatusDown, err.Error(), map[string]string{"endpoint": e.Endpoint}
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return health.StatusOK, "ComfyUI 可用", map[string]string{"endpoint": e.Endpoint, "status": resp.Status}
		}
		return health.StatusDegraded, resp.Status, map[string]string{"endpoint": e.Endpoint}
	})(ctx)
}

func (e *Engine) queue(ctx context.Context, endpoint string, workflow json.RawMessage) (string, error) {
	payload := map[string]any{
		"client_id": e.ClientID,
		"prompt":    json.RawMessage(workflow),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/prompt", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	resp, err := e.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("comfyui prompt 失败: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var body struct {
		PromptID string `json:"prompt_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.PromptID == "" {
		return "", errors.New("comfyui 未返回 prompt_id")
	}
	return body.PromptID, nil
}

func (e *Engine) waitImage(ctx context.Context, endpoint string, promptID string) (imageRef, error) {
	ticker := time.NewTicker(1200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(5 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return imageRef{}, ctx.Err()
		case <-deadline:
			return imageRef{}, errors.New("等待 comfyui 图片生成超时")
		case <-ticker.C:
			ref, ok, err := e.history(ctx, endpoint, promptID)
			if err != nil {
				return imageRef{}, err
			}
			if ok {
				return ref, nil
			}
		}
	}
}

func (e *Engine) history(ctx context.Context, endpoint string, promptID string) (imageRef, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/history/"+url.PathEscape(promptID), nil)
	if err != nil {
		return imageRef{}, false, err
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return imageRef{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return imageRef{}, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return imageRef{}, false, fmt.Errorf("comfyui history 失败: %s", resp.Status)
	}
	var body map[string]struct {
		Outputs map[string]struct {
			Images []imageRef `json:"images"`
		} `json:"outputs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return imageRef{}, false, err
	}
	item, ok := body[promptID]
	if !ok {
		return imageRef{}, false, nil
	}
	for _, output := range item.Outputs {
		if len(output.Images) > 0 {
			return output.Images[0], true, nil
		}
	}
	return imageRef{}, false, nil
}

func (e *Engine) download(ctx context.Context, endpoint string, ref imageRef) (string, error) {
	values := url.Values{}
	values.Set("filename", ref.Filename)
	values.Set("subfolder", ref.Subfolder)
	values.Set("type", first(ref.Type, "output"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/view?"+values.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("comfyui view 失败: %s", resp.Status)
	}
	if err := os.MkdirAll(e.OutputDir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%d-%s", time.Now().UnixNano(), filepath.Base(ref.Filename))
	file, err := os.Create(filepath.Join(e.OutputDir, name))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(file, resp.Body); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return name, nil
}

func (e *Engine) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return http.DefaultClient
}

type imageRef struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func extension(filename string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), ".")
	if ext == "" {
		return "png"
	}
	return ext
}
