package speech

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	HTTPClient       *http.Client
	Timeout          time.Duration
	MaxResponseBytes int
}

type TrainVoiceRequest struct {
	SpeakerID   string            `json:"speakerId"`
	AudioData   string            `json:"audioData"`
	AudioFormat string            `json:"audioFormat"`
	Language    int               `json:"language"`
	ExtraParams map[string]string `json:"extraParams,omitempty"`
}

type VoiceOperationRequest struct {
	SpeakerID string `json:"speakerId"`
}

type VoiceResult struct {
	HTTPStatus             int                `json:"httpStatus"`
	LogID                  string             `json:"logid"`
	SpeakerID              string             `json:"speakerId"`
	Status                 int                `json:"status"`
	AvailableTrainingTimes int                `json:"availableTrainingTimes"`
	CreateTime             int64              `json:"createTime"`
	Language               int                `json:"language"`
	SpeakerStatus          []VoiceModelStatus `json:"speakerStatus"`
	Code                   string             `json:"code"`
	Message                string             `json:"message"`
	RawJSON                string             `json:"rawJson"`
}

type VoiceModelStatus struct {
	ModelType int    `json:"modelType"`
	DemoAudio string `json:"demoAudio"`
}

type providerTrainRequest struct {
	SpeakerID   string            `json:"speaker_id"`
	Audio       providerAudio     `json:"audio"`
	Language    int               `json:"language"`
	ExtraParams map[string]string `json:"extra_params,omitempty"`
}

type providerAudio struct {
	Data   string `json:"data"`
	Format string `json:"format"`
}

type providerSpeakerRequest struct {
	SpeakerID string `json:"speaker_id"`
}

type providerVoiceResponse struct {
	AvailableTrainingTimes int                        `json:"available_training_times"`
	CreateTime             int64                      `json:"create_time"`
	Language               int                        `json:"language"`
	SpeakerID              string                     `json:"speaker_id"`
	SpeakerStatus          []providerVoiceModelStatus `json:"speaker_status"`
	Status                 int                        `json:"status"`
	Code                   any                        `json:"code,omitempty"`
	Message                string                     `json:"message,omitempty"`
	LogID                  string                     `json:"logid,omitempty"`
}

type providerVoiceModelStatus struct {
	ModelType int    `json:"model_type"`
	DemoAudio string `json:"demo_audio"`
}

func NewClient() *Client {
	return &Client{Timeout: 30 * time.Second, MaxResponseBytes: DefaultMaxProviderBytes}
}

func (c *Client) TrainVoice(ctx context.Context, settings Settings, credentials Credentials, request TrainVoiceRequest) (VoiceResult, error) {
	settings = withDefaults(settings)
	request = normalizeTrainRequest(settings, request)
	if err := validateTrainRequest(request); err != nil {
		return VoiceResult{}, err
	}
	body := providerTrainRequest{
		SpeakerID: request.SpeakerID,
		Audio: providerAudio{
			Data:   request.AudioData,
			Format: request.AudioFormat,
		},
		Language:    request.Language,
		ExtraParams: request.ExtraParams,
	}
	return c.doJSON(ctx, settings, credentials, settings.TrainPath, body)
}

func (c *Client) QueryVoice(ctx context.Context, settings Settings, credentials Credentials, request VoiceOperationRequest) (VoiceResult, error) {
	settings = withDefaults(settings)
	request.SpeakerID = defaultString(request.SpeakerID, settings.DefaultSpeaker)
	if strings.TrimSpace(request.SpeakerID) == "" {
		return VoiceResult{}, ErrSpeakerIDRequired
	}
	return c.doJSON(ctx, settings, credentials, settings.QueryPath, providerSpeakerRequest{SpeakerID: strings.TrimSpace(request.SpeakerID)})
}

func (c *Client) UpgradeVoice(ctx context.Context, settings Settings, credentials Credentials, request VoiceOperationRequest) (VoiceResult, error) {
	settings = withDefaults(settings)
	request.SpeakerID = defaultString(request.SpeakerID, settings.DefaultSpeaker)
	if strings.TrimSpace(request.SpeakerID) == "" {
		return VoiceResult{}, ErrSpeakerIDRequired
	}
	return c.doJSON(ctx, settings, credentials, settings.UpgradePath, providerSpeakerRequest{SpeakerID: strings.TrimSpace(request.SpeakerID)})
}

func (c *Client) doJSON(ctx context.Context, settings Settings, credentials Credentials, path string, payload any) (VoiceResult, error) {
	settings = withDefaults(settings)
	if err := validateReady(settings, credentials.HasAPIKey, credentials.HasAccessToken); err != nil {
		return VoiceResult{}, err
	}
	endpoint, err := joinEndpoint(settings.BaseURL, path)
	if err != nil {
		return VoiceResult{}, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return VoiceResult{}, fmt.Errorf("encoding volcengine voice clone request: %w", err)
	}
	requestID := newRequestID()
	reqCtx := ctx
	if c.timeout() > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, c.timeout())
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return VoiceResult{}, fmt.Errorf("creating volcengine voice clone request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Request-Id", requestID)
	secrets := applyCredentialHeaders(req.Header, settings, credentials)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return VoiceResult{}, sanitizeProviderError("sending volcengine voice clone request", err, secrets, "")
	}
	defer resp.Body.Close()
	data, err := readLimited(resp.Body, c.maxResponseBytes())
	if err != nil {
		return VoiceResult{}, sanitizeProviderError("reading volcengine voice clone response", err, secrets, resp.Header.Get("X-Tt-Logid"))
	}
	logID := defaultString(resp.Header.Get("X-Tt-Logid"), resp.Header.Get("X-Tt-Logid"))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return VoiceResult{}, providerHTTPError(resp.StatusCode, logID, data, secrets)
	}
	result, err := parseVoiceResult(resp.StatusCode, logID, data, secrets)
	if err != nil {
		return VoiceResult{}, err
	}
	return result, nil
}

func normalizeTrainRequest(settings Settings, request TrainVoiceRequest) TrainVoiceRequest {
	request.SpeakerID = defaultString(request.SpeakerID, settings.DefaultSpeaker)
	request.AudioFormat = normalizeFormat(defaultString(request.AudioFormat, settings.DefaultFormat))
	if request.Language < 0 {
		request.Language = settings.DefaultLanguage
	}
	request.AudioData = strings.TrimSpace(request.AudioData)
	return request
}

func validateTrainRequest(request TrainVoiceRequest) error {
	if strings.TrimSpace(request.SpeakerID) == "" {
		return ErrSpeakerIDRequired
	}
	if strings.TrimSpace(request.AudioData) == "" {
		return ErrAudioDataRequired
	}
	if _, err := base64.StdEncoding.DecodeString(request.AudioData); err != nil {
		return fmt.Errorf("audio data must be base64: %w", err)
	}
	if strings.TrimSpace(request.AudioFormat) == "" {
		return ErrAudioFormatRequired
	}
	if request.Language < 0 {
		return fmt.Errorf("language must be >= 0")
	}
	return nil
}

func applyCredentialHeaders(header http.Header, settings Settings, credentials Credentials) []string {
	var secrets []string
	if credentials.HasAPIKey {
		apiKey := credentials.APIKey.Expose()
		header.Set("X-Api-Key", apiKey)
		secrets = append(secrets, apiKey)
		return secrets
	}
	header.Set("X-Api-App-Key", settings.AppID)
	if credentials.HasAccessToken {
		accessToken := credentials.AccessToken.Expose()
		header.Set("X-Api-Access-Key", accessToken)
		secrets = append(secrets, accessToken)
	}
	return secrets
}

func parseVoiceResult(httpStatus int, logID string, data []byte, secrets []string) (VoiceResult, error) {
	raw := strings.TrimSpace(string(data))
	var provider providerVoiceResponse
	if err := json.Unmarshal(data, &provider); err != nil {
		return VoiceResult{}, providerError("volcengine voice clone malformed provider response", data, secrets)
	}
	models := make([]VoiceModelStatus, 0, len(provider.SpeakerStatus))
	for _, item := range provider.SpeakerStatus {
		models = append(models, VoiceModelStatus{ModelType: item.ModelType, DemoAudio: item.DemoAudio})
	}
	return VoiceResult{
		HTTPStatus:             httpStatus,
		LogID:                  firstNonEmpty(logID, provider.LogID),
		SpeakerID:              provider.SpeakerID,
		Status:                 provider.Status,
		AvailableTrainingTimes: provider.AvailableTrainingTimes,
		CreateTime:             provider.CreateTime,
		Language:               provider.Language,
		SpeakerStatus:          models,
		Code:                   codeToString(provider.Code),
		Message:                sanitizeText(provider.Message, secrets),
		RawJSON:                sanitizeText(raw, secrets),
	}, nil
}

func providerHTTPError(status int, logID string, payload []byte, secrets []string) error {
	prefix := fmt.Sprintf("volcengine voice clone http %d", status)
	if logID != "" {
		prefix += " logid=" + sanitizeText(logID, secrets)
	}
	return providerError(prefix, payload, secrets)
}

func providerError(prefix string, payload []byte, secrets []string) error {
	var msg providerVoiceResponse
	if err := json.Unmarshal(payload, &msg); err == nil && (msg.Message != "" || msg.Code != nil || msg.LogID != "") {
		return fmt.Errorf("%s: code=%s message=%s logid=%s", prefix, codeToString(msg.Code), sanitizeText(msg.Message, secrets), sanitizeText(msg.LogID, secrets))
	}
	summary := sanitizeText(string(bytes.TrimSpace(payload)), secrets)
	if len(summary) > 240 {
		summary = summary[:240] + "..."
	}
	if summary == "" {
		summary = "empty response"
	}
	return fmt.Errorf("%s: %s", prefix, summary)
}

func sanitizeProviderError(action string, err error, secrets []string, logID string) error {
	if err == nil {
		return nil
	}
	message := sanitizeText(err.Error(), secrets)
	if logID != "" {
		return fmt.Errorf("%s: %s logid=%s", action, message, sanitizeText(logID, secrets))
	}
	return fmt.Errorf("%s: %s", action, message)
}

func sanitizeText(value string, secrets []string) string {
	value = strings.ReplaceAll(value, "Authorization", "[REDACTED_HEADER]")
	value = strings.ReplaceAll(value, "X-Api-Key", "[REDACTED_HEADER]")
	value = strings.ReplaceAll(value, "X-Api-Access-Key", "[REDACTED_HEADER]")
	value = strings.ReplaceAll(value, "Bearer;", "Bearer;[REDACTED]")
	for _, secretValue := range secrets {
		if secretValue != "" {
			value = strings.ReplaceAll(value, secretValue, "[REDACTED]")
		}
	}
	return value
}

func (c *Client) httpClient() *http.Client {
	if c != nil && c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) timeout() time.Duration {
	if c == nil || c.Timeout == 0 {
		return 30 * time.Second
	}
	return c.Timeout
}

func (c *Client) maxResponseBytes() int {
	if c == nil || c.MaxResponseBytes <= 0 {
		return DefaultMaxProviderBytes
	}
	return c.MaxResponseBytes
}

func readLimited(reader io.Reader, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxProviderBytes
	}
	limited := io.LimitReader(reader, int64(maxBytes)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("provider response exceeded %d bytes", maxBytes)
	}
	return data, nil
}

func joinEndpoint(baseURL string, path string) (string, error) {
	baseURL = trimTrailingSlash(baseURL)
	path = normalizePath(path)
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parsing volcengine voice clone base_url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("volcengine voice clone base_url must include scheme and host")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	return parsed.String(), nil
}

func codeToString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func newRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err == nil {
		return hex.EncodeToString(buf[:])
	}
	return fmt.Sprintf("fairy-%d", time.Now().UnixNano())
}
