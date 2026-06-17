package volcengine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/health"
	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app"
)

const (
	DefaultEndpoint        = "https://openspeech.bytedance.com/api/v3/tts/unidirectional"
	DefaultV1Endpoint      = "https://openspeech.bytedance.com/api/v1/tts"
	DefaultOutputDir       = "tmp/audio"
	DefaultBaseURL         = "/audio/"
	DefaultResourceID      = DefaultCloneResourceID
	DefaultCloneResourceID = "seed-icl-2.0"
	DefaultCluster         = "volcano_tts"
	DefaultSpeaker         = "zh_female_vv_uranus_bigtts"
	DefaultFormat          = "mp3"
	DefaultSampleRate      = 24000
	DefaultUserID          = "fairy"
	DefaultCloneMaxBytes   = 10 * 1024 * 1024
	SuccessCode            = 20000000
	V1SuccessCode          = 3000
)

var (
	DefaultCloneURL = "https://openspeech.bytedance.com/api/v3/tts/voice_clone"
	DefaultVoiceURL = "https://openspeech.bytedance.com/api/v3/tts/get_voice"
)

type Engine struct {
	Endpoint   string
	APIKey     string
	ResourceID string
	Speaker    string
	Format     string
	UserID     string
	OutputDir  string
	BaseURL    string
	SampleRate int
	Client     *http.Client
}

type Options struct {
	Endpoint   string
	APIKey     string
	ResourceID string
	Speaker    string
	Format     string
	UserID     string
	OutputDir  string
	BaseURL    string
	SampleRate int
	Timeout    time.Duration
}

type requestBody struct {
	User      userConfig    `json:"user"`
	ReqParams requestParams `json:"req_params"`
}

type v1RequestBody struct {
	App     v1AppConfig     `json:"app"`
	User    userConfig      `json:"user"`
	Audio   v1AudioConfig   `json:"audio"`
	Request v1RequestConfig `json:"request"`
}

type v1AppConfig struct {
	AppID   string `json:"appid"`
	Token   string `json:"token"`
	Cluster string `json:"cluster"`
}

type v1AudioConfig struct {
	VoiceType   string  `json:"voice_type"`
	Encoding    string  `json:"encoding"`
	SpeedRatio  float64 `json:"speed_ratio"`
	VolumeRatio float64 `json:"volume_ratio"`
	PitchRatio  float64 `json:"pitch_ratio"`
}

type v1RequestConfig struct {
	ReqID     string `json:"reqid"`
	Text      string `json:"text"`
	TextType  string `json:"text_type"`
	Operation string `json:"operation"`
}

type userConfig struct {
	UID string `json:"uid"`
}

type requestParams struct {
	Text        string      `json:"text"`
	Speaker     string      `json:"speaker"`
	AudioParams audioParams `json:"audio_params"`
	Additions   string      `json:"additions,omitempty"`
}

type audioParams struct {
	Format       string `json:"format"`
	SampleRate   int    `json:"sample_rate,omitempty"`
	SpeechRate   int    `json:"speech_rate,omitempty"`
	LoudnessRate int    `json:"loudness_rate,omitempty"`
	Emotion      string `json:"emotion,omitempty"`
}

type additionsMap map[string]string

type responseFrame struct {
	Code     int    `json:"code,omitempty"`
	Message  string `json:"message,omitempty"`
	Data     string `json:"data,omitempty"`
	Sequence int    `json:"sequence,omitempty"`
}

type v1Response struct {
	ReqID     string `json:"reqid"`
	Code      int    `json:"code"`
	Operation string `json:"operation"`
	Message   string `json:"message"`
	Sequence  int    `json:"sequence"`
	Data      string `json:"data"`
}

type cloneRequestBody struct {
	SpeakerID string     `json:"speaker_id"`
	Language  int        `json:"language"`
	Audio     cloneAudio `json:"audio"`
}

type cloneAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

type cloneStatusRequestBody struct {
	SpeakerID string `json:"speaker_id"`
}

type cloneResponse struct {
	Code      int             `json:"code,omitempty"`
	Message   string          `json:"message,omitempty"`
	SpeakerID string          `json:"speaker_id,omitempty"`
	Status    int             `json:"status,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

func NewEngine(options Options) *Engine {
	if options.Endpoint == "" {
		options.Endpoint = DefaultEndpoint
	}
	if options.ResourceID == "" {
		options.ResourceID = DefaultResourceID
	}
	if options.Speaker == "" {
		options.Speaker = DefaultSpeaker
	}
	if options.Format == "" {
		options.Format = DefaultFormat
	}
	if options.UserID == "" {
		options.UserID = DefaultUserID
	}
	if options.OutputDir == "" {
		options.OutputDir = DefaultOutputDir
	}
	if options.BaseURL == "" {
		options.BaseURL = DefaultBaseURL
	}
	if options.SampleRate <= 0 {
		options.SampleRate = DefaultSampleRate
	}
	if options.Timeout <= 0 {
		options.Timeout = 60 * time.Second
	}
	return &Engine{
		Endpoint:   options.Endpoint,
		APIKey:     options.APIKey,
		ResourceID: options.ResourceID,
		Speaker:    options.Speaker,
		Format:     strings.TrimPrefix(options.Format, "."),
		UserID:     options.UserID,
		OutputDir:  options.OutputDir,
		BaseURL:    options.BaseURL,
		SampleRate: options.SampleRate,
		Client:     &http.Client{Timeout: options.Timeout},
	}
}

func (e *Engine) Synthesize(ctx context.Context, input voice.Input) (app.AudioResult, error) {
	text := strings.TrimSpace(input.Text)
	settings := e.settings(input.Profile)
	if text == "" {
		return app.AudioResult{Format: settings.Format, Placeholder: true}, nil
	}
	if err := settings.validate(); err != nil {
		return app.AudioResult{}, err
	}
	if settings.AccessToken != "" || settings.AppID != "" {
		if settings.APIVersion == "v1" {
			return e.synthesizeV1(ctx, text, input, settings)
		}
		return e.synthesizeV3WithAppToken(ctx, text, input, settings)
	}

	payload := requestBody{
		User: userConfig{UID: settings.UserID},
		ReqParams: requestParams{
			Text:    text,
			Speaker: settings.Speaker,
			AudioParams: audioParams{
				Format:       settings.Format,
				SampleRate:   settings.SampleRate,
				SpeechRate:   speechRate(input.Plan.Speed),
				LoudnessRate: extraInt(input.Profile, "loudness_rate", 0),
				Emotion:      first(input.Profile.Extra["emotion"], input.Plan.Style, input.Emotion),
			},
			Additions: additions(input.Profile),
		},
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return app.AudioResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, settings.Endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return app.AudioResult{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Api-Key", settings.APIKey)
	req.Header.Set("X-Api-Resource-Id", settings.ResourceID)
	req.Header.Set("X-Api-Request-Id", reqID())

	resp, err := e.client().Do(req)
	if err != nil {
		return app.AudioResult{}, err
	}
	defer resp.Body.Close()

	logID := resp.Header.Get("X-Tt-Logid")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return app.AudioResult{}, fmt.Errorf("volcengine v3 tts 失败: %s logid=%s: %s", resp.Status, logID, strings.TrimSpace(string(msg)))
	}

	audio, err := decodeChunkedFrames(resp.Body)
	if err != nil {
		return app.AudioResult{}, fmt.Errorf("volcengine v3 tts 解析失败 logid=%s: %w", logID, err)
	}
	if len(audio) == 0 {
		return app.AudioResult{}, fmt.Errorf("volcengine v3 tts 返回空音频 logid=%s", logID)
	}

	if err := os.MkdirAll(e.OutputDir, 0o755); err != nil {
		return app.AudioResult{}, err
	}
	name := fmt.Sprintf("%d.%s", time.Now().UnixNano(), extension(settings.Format))
	path := filepath.Join(e.OutputDir, name)
	if err := os.WriteFile(path, audio, 0o644); err != nil {
		return app.AudioResult{}, err
	}

	return app.AudioResult{
		URL:         e.BaseURL + name,
		Format:      settings.Format,
		DurationMS:  estimateDuration(text),
		Placeholder: false,
	}, nil
}

func (e *Engine) synthesizeV3WithAppToken(ctx context.Context, text string, input voice.Input, settings settings) (app.AudioResult, error) {
	payload := requestBody{
		User: userConfig{UID: settings.UserID},
		ReqParams: requestParams{
			Text:    text,
			Speaker: settings.Speaker,
			AudioParams: audioParams{
				Format:       settings.Format,
				SampleRate:   settings.SampleRate,
				SpeechRate:   speechRate(input.Plan.Speed),
				LoudnessRate: extraInt(input.Profile, "loudness_rate", 0),
				Emotion:      first(input.Profile.Extra["emotion"], input.Plan.Style, input.Emotion),
			},
			Additions: additions(input.Profile),
		},
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return app.AudioResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, settings.Endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return app.AudioResult{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Api-App-Key", settings.AppID)
	req.Header.Set("X-Api-Access-Key", settings.AccessToken)
	req.Header.Set("X-Api-Resource-Id", settings.ResourceID)
	req.Header.Set("X-Api-Request-Id", reqID())

	resp, err := e.client().Do(req)
	if err != nil {
		return app.AudioResult{}, err
	}
	defer resp.Body.Close()

	logID := resp.Header.Get("X-Tt-Logid")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return app.AudioResult{}, fmt.Errorf("volcengine v3 tts 失败: %s logid=%s: %s", resp.Status, logID, strings.TrimSpace(string(msg)))
	}

	audio, err := decodeChunkedFrames(resp.Body)
	if err != nil {
		return app.AudioResult{}, fmt.Errorf("volcengine v3 tts 解析失败 logid=%s: %w", logID, err)
	}
	if len(audio) == 0 {
		return app.AudioResult{}, fmt.Errorf("volcengine v3 tts 返回空音频 logid=%s", logID)
	}

	if err := os.MkdirAll(e.OutputDir, 0o755); err != nil {
		return app.AudioResult{}, err
	}
	name := fmt.Sprintf("%d.%s", time.Now().UnixNano(), extension(settings.Format))
	path := filepath.Join(e.OutputDir, name)
	if err := os.WriteFile(path, audio, 0o644); err != nil {
		return app.AudioResult{}, err
	}

	return app.AudioResult{
		URL:         e.BaseURL + name,
		Format:      settings.Format,
		DurationMS:  estimateDuration(text),
		Placeholder: false,
	}, nil
}

func (e *Engine) synthesizeV1(ctx context.Context, text string, input voice.Input, settings settings) (app.AudioResult, error) {
	payload := v1RequestBody{
		App: v1AppConfig{
			AppID:   settings.AppID,
			Token:   "access_token",
			Cluster: settings.Cluster,
		},
		User: userConfig{UID: settings.UserID},
		Audio: v1AudioConfig{
			VoiceType:   settings.Speaker,
			Encoding:    settings.Format,
			SpeedRatio:  speedRatio(input.Plan.Speed),
			VolumeRatio: 1,
			PitchRatio:  1,
		},
		Request: v1RequestConfig{
			ReqID:     reqID(),
			Text:      text,
			TextType:  "plain",
			Operation: "query",
		},
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return app.AudioResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, settings.Endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return app.AudioResult{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("Authorization", "Bearer;"+settings.AccessToken)

	resp, err := e.client().Do(req)
	if err != nil {
		return app.AudioResult{}, err
	}
	defer resp.Body.Close()

	logID := resp.Header.Get("X-Tt-Logid")
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if err != nil {
		return app.AudioResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return app.AudioResult{}, v1ProviderError(resp.Status, logID, body)
	}

	var out v1Response
	if err := json.Unmarshal(body, &out); err != nil {
		return app.AudioResult{}, fmt.Errorf("volcengine v1 tts 解析失败 logid=%s: %w", logID, err)
	}
	if out.Code != V1SuccessCode {
		return app.AudioResult{}, fmt.Errorf("volcengine v1 tts 失败: code=%d message=%s logid=%s%s", out.Code, out.Message, logID, v1Hint(out.Code, out.Message))
	}
	audio, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		return app.AudioResult{}, fmt.Errorf("volcengine v1 tts 音频解码失败 logid=%s: %w", logID, err)
	}
	if len(audio) == 0 {
		return app.AudioResult{}, fmt.Errorf("volcengine v1 tts 返回空音频 logid=%s", logID)
	}

	if err := os.MkdirAll(e.OutputDir, 0o755); err != nil {
		return app.AudioResult{}, err
	}
	name := fmt.Sprintf("%d.%s", time.Now().UnixNano(), extension(settings.Format))
	path := filepath.Join(e.OutputDir, name)
	if err := os.WriteFile(path, audio, 0o644); err != nil {
		return app.AudioResult{}, err
	}

	return app.AudioResult{
		URL:         e.BaseURL + name,
		Format:      settings.Format,
		DurationMS:  estimateDuration(text),
		Placeholder: false,
	}, nil
}

func (e *Engine) CloneVoice(ctx context.Context, request app.VoiceCloneRequest) (app.VoiceCloneResult, error) {
	if err := validateCloneRequest(request, true); err != nil {
		return app.VoiceCloneResult{}, err
	}

	result := app.VoiceCloneResult{
		SpeakerID:   request.SpeakerID,
		ResourceID:  request.ResourceID,
		SampleCount: len(request.Samples),
		SampleLogs:  make([]string, 0, len(request.Samples)),
	}
	language, err := cloneLanguage(request.Language)
	if err != nil {
		return app.VoiceCloneResult{}, err
	}

	sample := request.Samples[0]
	body := cloneRequestBody{
		SpeakerID: request.SpeakerID,
		Language:  language,
		Audio: cloneAudio{
			Data:   sample.DataBase64,
			Format: cloneSampleFormat(sample),
		},
	}
	resp, logID, err := e.postClone(ctx, DefaultCloneURL, request, body)
	if logID != "" {
		result.LogID = logID
	}
	if err != nil {
		return result, fmt.Errorf("volcengine 声音复刻训练失败 sample=1 logid=%s: %w", logID, err)
	}
	result.StatusCode = resp.Status
	result.Status = cloneStatusText(resp.Status)
	result.Message = first(resp.Message, "训练请求已提交")
	result.SampleLogs = append(result.SampleLogs, fmt.Sprintf("%s: %s", first(sample.Filename, "sample-1"), result.Message))
	if result.Status == "" {
		result.Status = "submitted"
	}
	return result, nil
}

func (e *Engine) CloneStatus(ctx context.Context, request app.VoiceCloneRequest) (app.VoiceCloneResult, error) {
	if err := validateCloneRequest(request, false); err != nil {
		return app.VoiceCloneResult{}, err
	}
	body := cloneStatusRequestBody{SpeakerID: request.SpeakerID}
	resp, logID, err := e.postClone(ctx, DefaultVoiceURL, request, body)
	if err != nil {
		return app.VoiceCloneResult{}, fmt.Errorf("volcengine 声音复刻状态查询失败 logid=%s: %w", logID, err)
	}
	speakerID := first(resp.SpeakerID, request.SpeakerID)
	return app.VoiceCloneResult{
		SpeakerID:  speakerID,
		ResourceID: request.ResourceID,
		Status:     cloneStatusText(resp.Status),
		StatusCode: resp.Status,
		Message:    resp.Message,
		LogID:      logID,
	}, nil
}

func (e *Engine) postClone(ctx context.Context, endpoint string, request app.VoiceCloneRequest, body any) (cloneResponse, string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return cloneResponse{}, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return cloneResponse{}, "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Api-App-Key", request.AppID)
	req.Header.Set("X-Api-Access-Key", request.AccessToken)
	req.Header.Set("X-Api-Resource-Id", request.ResourceID)
	req.Header.Set("X-Api-Request-Id", reqID())

	httpResp, err := e.client().Do(req)
	if err != nil {
		return cloneResponse{}, "", err
	}
	defer httpResp.Body.Close()

	logID := httpResp.Header.Get("X-Tt-Logid")
	bodyBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, 2*1024*1024))
	if err != nil {
		return cloneResponse{}, logID, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return cloneResponse{}, logID, fmt.Errorf("%s: %s", httpResp.Status, strings.TrimSpace(string(bodyBytes)))
	}
	var out cloneResponse
	if err := json.Unmarshal(bodyBytes, &out); err != nil {
		return cloneResponse{}, logID, fmt.Errorf("解析响应失败: %w", err)
	}
	if out.Code != 0 && out.Code != SuccessCode {
		return out, logID, fmt.Errorf("code=%d message=%s", out.Code, out.Message)
	}
	if out.Data != nil {
		out = mergeCloneData(out)
	}
	return out, logID, nil
}

func v1ProviderError(status string, logID string, body []byte) error {
	var out v1Response
	if err := json.Unmarshal(body, &out); err == nil && (out.Code != 0 || out.Message != "") {
		return fmt.Errorf("volcengine v1 tts 失败: %s code=%d message=%s logid=%s%s", status, out.Code, out.Message, logID, v1Hint(out.Code, out.Message))
	}
	return fmt.Errorf("volcengine v1 tts 失败: %s logid=%s: %s", status, logID, strings.TrimSpace(string(body)))
}

func v1Hint(code int, message string) string {
	if code == 3031 || strings.Contains(message, "Init Engine Instance failed") {
		return "；请检查 voice_type 和 cluster 是否匹配。APP ID 不是 cluster，常见 cluster 为 volcano_tts，voice_type 应填写控制台音色详情里的 Voice_type"
	}
	if code == 3001 || strings.Contains(message, "resource not granted") {
		return "；请检查当前应用是否开通该 TTS 服务、cluster 是否填写为 volcano_tts，以及 voice_type 是否属于该应用"
	}
	if strings.Contains(message, "unsupported language") {
		return "；当前音色不支持这段文本语言，请切换对应语种的 voice_type，或改用该音色支持的文本语言"
	}
	return ""
}

func validateCloneRequest(request app.VoiceCloneRequest, requireSamples bool) error {
	if strings.TrimSpace(request.AppID) == "" {
		return errors.New("volcengine voice clone app_id 不能为空")
	}
	if request.AccessToken == "" {
		return errors.New("volcengine voice clone access_token 不能为空")
	}
	if !strings.HasPrefix(strings.TrimSpace(request.ResourceID), "seed-icl-") {
		return errors.New("volcengine voice clone resource_id 必须使用 seed-icl-*")
	}
	if strings.TrimSpace(request.SpeakerID) == "" {
		return errors.New("volcengine voice clone speaker_id 不能为空")
	}
	if strings.TrimSpace(request.Language) == "" {
		return errors.New("volcengine voice clone language 不能为空")
	}
	if _, err := cloneLanguage(request.Language); err != nil {
		return err
	}
	if !requireSamples {
		return nil
	}
	if len(request.Samples) == 0 {
		return errors.New("volcengine voice clone 至少需要一个训练音频")
	}
	if len(request.Samples) > 1 {
		return errors.New("volcengine voice clone v3 一次训练只接受 1 段 audio；请先选择一段参考音频")
	}
	for index, sample := range request.Samples {
		if strings.TrimSpace(sample.DataBase64) == "" {
			return fmt.Errorf("volcengine voice clone sample[%d].data_base64 不能为空", index)
		}
		decoded, err := base64.StdEncoding.DecodeString(sample.DataBase64)
		if err != nil {
			return fmt.Errorf("volcengine voice clone sample[%d].data_base64 不是合法 base64: %w", index, err)
		}
		if len(decoded) > DefaultCloneMaxBytes {
			return fmt.Errorf("volcengine voice clone sample[%d] 超过 10MB", index)
		}
		if cloneSampleFormat(sample) == "" {
			return fmt.Errorf("volcengine voice clone sample[%d].format 不能为空", index)
		}
	}
	return nil
}

func cloneLanguage(value string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "zh", "cn", "zh-cn":
		return 0, nil
	case "1", "en":
		return 1, nil
	case "2", "ja", "jp":
		return 2, nil
	case "3", "es":
		return 3, nil
	case "4", "id":
		return 4, nil
	case "5", "pt":
		return 5, nil
	case "6", "de":
		return 6, nil
	case "7", "fr":
		return 7, nil
	case "8", "ko", "kr":
		return 8, nil
	default:
		return 0, fmt.Errorf("volcengine voice clone language 不支持: %q", value)
	}
}

func cloneSampleFormat(sample app.VoiceCloneSample) string {
	format := strings.ToLower(strings.TrimSpace(sample.Format))
	if format != "" {
		return strings.TrimPrefix(format, ".")
	}
	name := strings.ToLower(sample.Filename)
	for _, ext := range []string{".wav", ".mp3", ".ogg", ".m4a", ".aac", ".pcm"} {
		if strings.HasSuffix(name, ext) {
			return strings.TrimPrefix(ext, ".")
		}
	}
	mimeType := strings.ToLower(sample.MimeType)
	switch {
	case strings.Contains(mimeType, "wav"):
		return "wav"
	case strings.Contains(mimeType, "mpeg"), strings.Contains(mimeType, "mp3"):
		return "mp3"
	case strings.Contains(mimeType, "ogg"):
		return "ogg"
	case strings.Contains(mimeType, "aac"):
		return "aac"
	case strings.Contains(mimeType, "mp4"), strings.Contains(mimeType, "m4a"):
		return "m4a"
	default:
		return ""
	}
}

func cloneStatusText(status int) string {
	switch status {
	case 1:
		return "training"
	case 2:
		return "success"
	case 3:
		return "failed"
	case 4:
		return "active"
	default:
		if status == 0 {
			return "submitted"
		}
		return fmt.Sprintf("unknown:%d", status)
	}
}

func mergeCloneData(out cloneResponse) cloneResponse {
	var data struct {
		SpeakerID string `json:"speaker_id"`
		Status    int    `json:"status"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(out.Data, &data); err != nil {
		return out
	}
	if out.SpeakerID == "" {
		out.SpeakerID = data.SpeakerID
	}
	if out.Status == 0 {
		out.Status = data.Status
	}
	if out.Message == "" {
		out.Message = data.Message
	}
	return out
}

func decodeChunkedFrames(reader io.Reader) ([]byte, error) {
	decoder := json.NewDecoder(reader)
	audio := make([]byte, 0)
	for {
		var frame responseFrame
		err := decoder.Decode(&frame)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if frame.Code != 0 && frame.Code != SuccessCode {
			return nil, fmt.Errorf("code=%d message=%s", frame.Code, frame.Message)
		}
		if frame.Data == "" {
			continue
		}
		chunk, err := base64.StdEncoding.DecodeString(frame.Data)
		if err != nil {
			return nil, err
		}
		audio = append(audio, chunk...)
	}
	return audio, nil
}

func (e *Engine) Check(ctx context.Context) health.Result {
	return health.Measure("voice", string(voice.ProviderVolcengine), func(ctx context.Context) (health.Status, string, map[string]string) {
		settings := e.settings(app.VoiceProfile{})
		if err := settings.validate(); err != nil {
			return health.StatusDown, err.Error(), map[string]string{"endpoint": settings.Endpoint}
		}
		return health.StatusOK, "Volcengine TTS 已配置", map[string]string{
			"endpoint":    settings.Endpoint,
			"resource_id": settings.ResourceID,
			"speaker":     settings.Speaker,
		}
	})(ctx)
}

type settings struct {
	Endpoint    string
	APIKey      string
	AppID       string
	AccessToken string
	APIVersion  string
	ResourceID  string
	Cluster     string
	Speaker     string
	Format      string
	UserID      string
	SampleRate  int
}

func (s settings) validate() error {
	if strings.TrimSpace(s.Endpoint) == "" {
		return errors.New("volcengine endpoint 不能为空")
	}
	if strings.TrimSpace(s.AccessToken) != "" || strings.TrimSpace(s.AppID) != "" {
		if strings.TrimSpace(s.AppID) == "" {
			return errors.New("volcengine app_id 不能为空")
		}
		if strings.TrimSpace(s.AccessToken) == "" {
			return errors.New("volcengine access_token 不能为空")
		}
		if s.APIVersion == "v1" && strings.TrimSpace(s.Cluster) == "" {
			return errors.New("volcengine cluster 不能为空")
		}
		if s.APIVersion != "v1" && strings.TrimSpace(s.ResourceID) == "" {
			return errors.New("volcengine resource_id 不能为空")
		}
		if strings.TrimSpace(s.Speaker) == "" {
			return errors.New("volcengine speaker 不能为空")
		}
		return nil
	}
	if strings.TrimSpace(s.APIKey) == "" {
		return errors.New("volcengine api_key 不能为空；如果使用 APP ID / Access Token，请传 extra.app_id 和 extra.access_token")
	}
	if strings.TrimSpace(s.ResourceID) == "" {
		return errors.New("volcengine resource_id 不能为空")
	}
	if strings.TrimSpace(s.Speaker) == "" {
		return errors.New("volcengine speaker 不能为空")
	}
	return nil
}

func (e *Engine) settings(profile app.VoiceProfile) settings {
	appID := first(profile.Extra["app_id"], profile.Extra["appid"])
	accessToken := first(profile.Extra["access_token"], profile.Extra["token"])
	apiVersion := strings.ToLower(first(profile.Extra["api_version"], "v3"))
	endpoint := first(profile.Endpoint, e.Endpoint)
	if strings.Contains(endpoint, "/api/v1/") {
		apiVersion = "v1"
	}
	if endpoint == e.Endpoint && (appID != "" || accessToken != "") && apiVersion == "v1" {
		endpoint = DefaultV1Endpoint
	}
	return settings{
		Endpoint:    endpoint,
		APIKey:      first(profile.Extra["api_key"], e.APIKey),
		AppID:       appID,
		AccessToken: accessToken,
		APIVersion:  apiVersion,
		ResourceID:  first(profile.Extra["resource_id"], e.ResourceID),
		Cluster:     first(profile.Extra["cluster"], DefaultCluster),
		Speaker:     first(profile.VoiceID, profile.Extra["speaker"], e.Speaker),
		Format:      strings.TrimPrefix(first(profile.MediaType, profile.Extra["format"], e.Format), "."),
		UserID:      first(profile.Extra["uid"], e.UserID),
		SampleRate:  e.sampleRate(profile),
	}
}

func (e *Engine) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return http.DefaultClient
}

func (e *Engine) sampleRate(profile app.VoiceProfile) int {
	raw := first(profile.Extra["sample_rate"])
	if raw == "" {
		return e.SampleRate
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return e.SampleRate
	}
	return value
}

func additions(profile app.VoiceProfile) string {
	out := additionsMap{}
	for _, key := range []string{"explicit_language", "explicit_dialect", "disable_markdown_filter", "silence_duration"} {
		if value := first(profile.Extra[key]); value != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return ""
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(raw)
}

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func speechRate(speed float64) int {
	if speed <= 0 || speed == 1 {
		return 0
	}
	value := int((speed - 1) * 100)
	if value < -50 {
		return -50
	}
	if value > 100 {
		return 100
	}
	return value
}

func speedRatio(speed float64) float64 {
	if speed <= 0 {
		return 1
	}
	return speed
}

func extraInt(profile app.VoiceProfile, key string, fallback int) int {
	raw := first(profile.Extra[key])
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func extension(format string) string {
	switch strings.ToLower(format) {
	case "pcm":
		return "pcm"
	case "ogg_opus":
		return "ogg"
	case "mp3", "ogg":
		return strings.ToLower(format)
	default:
		return "mp3"
	}
}

func reqID() string {
	return fmt.Sprintf("fairy-%d", time.Now().UnixNano())
}

func estimateDuration(text string) int {
	n := len([]rune(text))
	if n == 0 {
		return 0
	}
	return 700 + n*95
}
