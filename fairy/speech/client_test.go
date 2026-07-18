package speech

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fairy/secret"
)

func TestClientTrainVoiceUsesHTTPHeadersAndBody(t *testing.T) {
	var gotPath string
	var gotHeaders http.Header
	var gotBody providerTrainRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("Decode(body) error = %v", err)
		}
		w.Header().Set("X-Tt-Logid", "log-train")
		_, _ = w.Write([]byte(`{"available_training_times":15,"create_time":1772026663000,"language":0,"speaker_id":"S_voice","speaker_status":[{"demo_audio":"https://x.bytespeech.com/S_voice","model_type":5}],"status":2}`))
	}))
	defer server.Close()

	apiKey, err := secret.NewValue("test-api-key")
	if err != nil {
		t.Fatalf("NewValue() error = %v", err)
	}
	audio := base64.StdEncoding.EncodeToString([]byte("fake wav"))
	result, err := (&Client{HTTPClient: server.Client(), Timeout: time.Second}).TrainVoice(context.Background(), Settings{
		Enabled:   true,
		BaseURL:   server.URL,
		TrainPath: DefaultTrainPath,
		AppID:     "appid",
	}, Credentials{APIKey: apiKey, HasAPIKey: true}, TrainVoiceRequest{
		SpeakerID:   "S_voice",
		AudioData:   audio,
		AudioFormat: "wav",
		Language:    0,
	})
	if err != nil {
		t.Fatalf("TrainVoice() error = %v", err)
	}
	if gotPath != "/api/v3/tts/voice_clone" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotHeaders.Get("X-Api-Key") != "test-api-key" || gotHeaders.Get("X-Api-Request-Id") == "" || gotHeaders.Get("Authorization") != "" || gotHeaders.Get("Resource-Id") != "" {
		t.Fatalf("headers = %#v", gotHeaders)
	}
	if gotBody.SpeakerID != "S_voice" || gotBody.Audio.Data != audio || gotBody.Audio.Format != "wav" || gotBody.Language != 0 {
		t.Fatalf("body = %#v", gotBody)
	}
	if result.HTTPStatus != http.StatusOK || result.LogID != "log-train" || result.SpeakerID != "S_voice" || result.Status != 2 || result.AvailableTrainingTimes != 15 {
		t.Fatalf("result = %#v", result)
	}
	if len(result.SpeakerStatus) != 1 || result.SpeakerStatus[0].ModelType != 5 {
		t.Fatalf("speaker status = %#v", result.SpeakerStatus)
	}
}

func TestClientQueryAndUpgradeUseAppAccessTokenHeaders(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("X-Api-Key") != "test-api-key" || r.Header.Get("X-Api-Request-Id") == "" {
			t.Fatalf("headers = %#v", r.Header)
		}
		var body providerSpeakerRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode(body) error = %v", err)
		}
		if body.SpeakerID != "S_voice" {
			t.Fatalf("body = %#v", body)
		}
		_, _ = w.Write([]byte(`{"speaker_id":"S_voice","status":2}`))
	}))
	defer server.Close()

	apiKey, err := secret.NewValue("test-api-key")
	if err != nil {
		t.Fatalf("NewValue() error = %v", err)
	}
	settings := Settings{Enabled: true, BaseURL: server.URL, AppID: "appid"}
	credentials := Credentials{APIKey: apiKey, HasAPIKey: true}
	client := &Client{HTTPClient: server.Client(), Timeout: time.Second}
	if _, err := client.QueryVoice(context.Background(), settings, credentials, VoiceOperationRequest{SpeakerID: "S_voice"}); err != nil {
		t.Fatalf("QueryVoice() error = %v", err)
	}
	if _, err := client.UpgradeVoice(context.Background(), settings, credentials, VoiceOperationRequest{SpeakerID: "S_voice"}); err != nil {
		t.Fatalf("UpgradeVoice() error = %v", err)
	}
	want := []string{"/api/v3/tts/get_voice", "/upgrade_voice"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}

func TestClientSynthesizeUsesHTTPBodyAndReturnsDataURL(t *testing.T) {
	var gotPath string
	var gotHeaders http.Header
	var gotBody providerSynthesisRequest
	audio1 := base64.StdEncoding.EncodeToString([]byte("fake "))
	audio2 := base64.StdEncoding.EncodeToString([]byte("mp3"))
	wantAudio := base64.StdEncoding.EncodeToString([]byte("fake mp3"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("Decode(body) error = %v", err)
		}
		w.Header().Set("X-Tt-Logid", "log-tts")
		_, _ = w.Write([]byte(`{"code":20000000,"message":"OK","data":"` + audio1 + `"}` + "\n" + `{"code":20000000,"message":"OK","data":"` + audio2 + `"}`))
	}))
	defer server.Close()
	token, err := secret.NewValue("access-token")
	if err != nil {
		t.Fatalf("NewValue() error = %v", err)
	}
	result, err := (&Client{HTTPClient: server.Client(), Timeout: time.Second}).SynthesizeSpeech(context.Background(), Settings{Enabled: true, BaseURL: server.URL, SynthesisResourceID: "seed-icl-1.0", DefaultSpeaker: "S_voice"}, Credentials{APIKey: token, HasAPIKey: true}, SynthesizeSpeechRequest{Text: "こんにちは。"})
	if err != nil {
		t.Fatalf("SynthesizeSpeech() error = %v", err)
	}
	if gotPath != DefaultSynthesizePath {
		t.Fatalf("path = %q", gotPath)
	}
	if gotHeaders.Get("X-Api-Key") != "access-token" || gotHeaders.Get("X-Api-App-Key") != "" || gotHeaders.Get("X-Api-Access-Key") != "" || gotHeaders.Get("X-Api-Resource-Id") != "seed-icl-1.0" || gotHeaders.Get("X-Api-Request-Id") == "" {
		t.Fatalf("headers = %#v", gotHeaders)
	}
	if gotBody.ReqParams.Speaker != "S_voice" || gotBody.ReqParams.Model != "" || gotBody.ReqParams.Text != "こんにちは。" || gotBody.ReqParams.AudioParams.Format != DefaultSynthesisFormat || gotBody.ReqParams.AudioParams.SampleRate != DefaultSynthesisSampleRate {
		t.Fatalf("body = %#v", gotBody)
	}
	if result.LogID != "log-tts" || result.SpeakerID != "S_voice" || result.MimeType != "audio/mpeg" || result.AudioBase64 != wantAudio || !strings.HasPrefix(result.DataURL, "data:audio/mpeg;base64,") {
		t.Fatalf("result = %#v", result)
	}
}

func TestClientSynthesizeRedactsProviderErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"Forbidden","message":"bad key test-api-key X-Api-Key X-Api-Resource-Id"}`))
	}))
	defer server.Close()
	apiKey, _ := secret.NewValue("test-api-key")
	_, err := (&Client{HTTPClient: server.Client(), Timeout: time.Second}).SynthesizeSpeech(context.Background(), Settings{Enabled: true, BaseURL: server.URL, DefaultSpeaker: "S_voice"}, Credentials{APIKey: apiKey, HasAPIKey: true}, SynthesizeSpeechRequest{Text: "こんにちは。"})
	if err == nil {
		t.Fatal("SynthesizeSpeech() error = nil, want provider error")
	}
	message := err.Error()
	if strings.Contains(message, "test-api-key") || strings.Contains(message, "X-Api-Key") || strings.Contains(message, "X-Api-Resource-Id") {
		t.Fatalf("error leaked secret/header: %q", message)
	}
}

func TestClientTrainVoiceRejectsInvalidInputBeforeProvider(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer server.Close()
	apiKey, _ := secret.NewValue("test-api-key")
	_, err := (&Client{HTTPClient: server.Client(), Timeout: time.Second}).TrainVoice(context.Background(), Settings{Enabled: true, BaseURL: server.URL}, Credentials{APIKey: apiKey, HasAPIKey: true}, TrainVoiceRequest{
		SpeakerID:   "S_voice",
		AudioData:   "not-base64",
		AudioFormat: "wav",
	})
	if err == nil {
		t.Fatal("TrainVoice() error = nil, want validation error")
	}
	if calls != 0 {
		t.Fatalf("provider calls = %d, want 0", calls)
	}
}

func TestClientRedactsProviderErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"Forbidden","message":"bad key test-api-key X-Api-Key"}`))
	}))
	defer server.Close()
	apiKey, _ := secret.NewValue("test-api-key")
	_, err := (&Client{HTTPClient: server.Client(), Timeout: time.Second}).QueryVoice(context.Background(), Settings{Enabled: true, BaseURL: server.URL}, Credentials{APIKey: apiKey, HasAPIKey: true}, VoiceOperationRequest{SpeakerID: "S_voice"})
	if err == nil {
		t.Fatal("QueryVoice() error = nil, want provider error")
	}
	message := err.Error()
	if strings.Contains(message, "test-api-key") || strings.Contains(message, "X-Api-Key") {
		t.Fatalf("error leaked secret/header: %q", message)
	}
	if !strings.Contains(message, "[REDACTED]") || !strings.Contains(message, "[REDACTED_HEADER]") {
		t.Fatalf("error = %q, want redaction markers", message)
	}
}
