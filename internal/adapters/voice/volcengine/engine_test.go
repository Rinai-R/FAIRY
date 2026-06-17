package volcengine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app"
)

func TestSynthesizeUsesV1AccessTokenAuthentication(t *testing.T) {
	t.Parallel()

	var gotAuthorization string
	var gotBody v1RequestBody
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		gotAuthorization = r.Header.Get("Authorization")
		if r.Header.Get("X-Api-Key") != "" {
			t.Fatalf("unexpected X-Api-Key header for v1 request")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(v1Response{
			ReqID:     gotBody.Request.ReqID,
			Code:      V1SuccessCode,
			Operation: "query",
			Message:   "Success",
			Sequence:  -1,
			Data:      base64.StdEncoding.EncodeToString([]byte("fake mp3 bytes")),
		})
	}))
	defer server.Close()

	engine := NewEngine(Options{
		Endpoint:  "https://openspeech.bytedance.com/api/v3/tts/unidirectional",
		OutputDir: filepath.Join(t.TempDir(), "audio"),
		BaseURL:   "/audio/",
		Timeout:   time.Second,
	})
	result, err := engine.Synthesize(context.Background(), voice.Input{
		Text: "你好，开始学习。",
		Plan: app.VoicePlan{
			VoiceID: "zh_female_vv_uranus_bigtts",
			Speed:   1.2,
		},
		Profile: app.VoiceProfile{
			Endpoint:  server.URL,
			VoiceID:   "zh_female_vv_uranus_bigtts",
			MediaType: "mp3",
			Extra: map[string]string{
				"app_id":       "9193177346",
				"access_token": "test-access-token",
				"api_version":  "v1",
				"cluster":      "volcano_tts",
				"uid":          "fairy-test",
			},
		},
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if gotAuthorization != "Bearer;test-access-token" {
		t.Fatalf("Authorization = %q, want Bearer;test-access-token", gotAuthorization)
	}
	if gotBody.App.AppID != "9193177346" {
		t.Fatalf("appid = %q, want 9193177346", gotBody.App.AppID)
	}
	if gotBody.App.Token != "access_token" {
		t.Fatalf("app token = %q, want access_token", gotBody.App.Token)
	}
	if gotBody.App.Cluster != "volcano_tts" {
		t.Fatalf("cluster = %q, want volcano_tts", gotBody.App.Cluster)
	}
	if gotBody.Audio.VoiceType != "zh_female_vv_uranus_bigtts" {
		t.Fatalf("voice_type = %q", gotBody.Audio.VoiceType)
	}
	if gotBody.Audio.SpeedRatio != 1.2 {
		t.Fatalf("speed_ratio = %v, want 1.2", gotBody.Audio.SpeedRatio)
	}
	if gotBody.Request.Operation != "query" || gotBody.Request.TextType != "plain" {
		t.Fatalf("request operation/text_type = %s/%s", gotBody.Request.Operation, gotBody.Request.TextType)
	}
	if result.Placeholder {
		t.Fatal("result.Placeholder = true, want false")
	}
	if result.URL == "" || !strings.HasPrefix(result.URL, "/audio/") {
		t.Fatalf("result.URL = %q, want /audio/...", result.URL)
	}
	if result.Format != "mp3" {
		t.Fatalf("result.Format = %q, want mp3", result.Format)
	}
}

func TestSynthesizeUsesV3AppTokenAuthentication(t *testing.T) {
	t.Parallel()

	var gotAppKey string
	var gotAccessKey string
	var gotResourceID string
	var gotBody requestBody
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAppKey = r.Header.Get("X-Api-App-Key")
		gotAccessKey = r.Header.Get("X-Api-Access-Key")
		gotResourceID = r.Header.Get("X-Api-Resource-Id")
		if r.Header.Get("X-Api-Key") != "" {
			t.Fatalf("unexpected X-Api-Key header for app token request")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(responseFrame{
			Code:    0,
			Message: "",
			Data:    base64.StdEncoding.EncodeToString([]byte("fake mp3 bytes")),
		})
	}))
	defer server.Close()

	engine := NewEngine(Options{
		Endpoint:  server.URL,
		OutputDir: filepath.Join(t.TempDir(), "audio"),
		BaseURL:   "/audio/",
		Timeout:   time.Second,
	})
	result, err := engine.Synthesize(context.Background(), voice.Input{
		Text: "你好，开始学习。",
		Plan: app.VoicePlan{
			VoiceID: "S_z5F1jrL52",
			Speed:   1,
		},
		Profile: app.VoiceProfile{
			VoiceID:   "S_z5F1jrL52",
			MediaType: "mp3",
			Extra: map[string]string{
				"app_id":       "9193177346",
				"access_token": "test-access-token",
				"resource_id":  "seed-icl-2.0",
			},
		},
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if gotAppKey != "9193177346" {
		t.Fatalf("X-Api-App-Key = %q", gotAppKey)
	}
	if gotAccessKey != "test-access-token" {
		t.Fatalf("X-Api-Access-Key = %q", gotAccessKey)
	}
	if gotResourceID != "seed-icl-2.0" {
		t.Fatalf("X-Api-Resource-Id = %q", gotResourceID)
	}
	if gotBody.ReqParams.Speaker != "S_z5F1jrL52" {
		t.Fatalf("speaker = %q", gotBody.ReqParams.Speaker)
	}
	if result.Placeholder {
		t.Fatal("result.Placeholder = true, want false")
	}
}

func TestSynthesizeV1RequiresBothAppIDAndAccessToken(t *testing.T) {
	t.Parallel()

	engine := NewEngine(Options{Endpoint: DefaultV1Endpoint})
	_, err := engine.Synthesize(context.Background(), voice.Input{
		Text: "你好",
		Profile: app.VoiceProfile{
			Extra: map[string]string{
				"app_id": "9193177346",
			},
		},
	})
	if err == nil {
		t.Fatal("Synthesize() error = nil, want missing access_token error")
	}
	if !strings.Contains(err.Error(), "access_token") {
		t.Fatalf("error = %q, want access_token", err.Error())
	}
}

func TestSynthesizeV1ExplainsClusterAndVoiceTypeErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(v1Response{
			ReqID:     "fairy-test",
			Code:      3031,
			Operation: "query",
			Message:   "Fail to feed text, reason Init Engine Instance failed",
		})
	}))
	defer server.Close()

	engine := NewEngine(Options{OutputDir: t.TempDir()})
	_, err := engine.Synthesize(context.Background(), voice.Input{
		Text: "你好",
		Profile: app.VoiceProfile{
			Endpoint: server.URL,
			VoiceID:  "zh_female_vv_uranus_bigtts",
			Extra: map[string]string{
				"app_id":       "9193177346",
				"access_token": "test-access-token",
				"api_version":  "v1",
				"cluster":      "wrong-cluster",
			},
		},
	})
	if err == nil {
		t.Fatal("Synthesize() error = nil, want provider error")
	}
	if !strings.Contains(err.Error(), "voice_type 和 cluster") {
		t.Fatalf("error = %q, want voice_type/cluster hint", err.Error())
	}
	if !strings.Contains(err.Error(), "volcano_tts") {
		t.Fatalf("error = %q, want volcano_tts hint", err.Error())
	}
}

func TestCloneVoiceUsesSeedICLResourceAndSpeakerID(t *testing.T) {
	var gotAppKey string
	var gotAccessKey string
	var gotResourceID string
	var gotBody cloneRequestBody
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAppKey = r.Header.Get("X-Api-App-Key")
		gotAccessKey = r.Header.Get("X-Api-Access-Key")
		gotResourceID = r.Header.Get("X-Api-Resource-Id")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_ = json.NewEncoder(w).Encode(cloneResponse{
			Code:      SuccessCode,
			Message:   "submitted",
			SpeakerID: gotBody.SpeakerID,
			Status:    1,
		})
	}))
	defer server.Close()

	engine := NewEngine(Options{OutputDir: t.TempDir(), Timeout: time.Second})
	oldCloneURL := DefaultCloneURL
	DefaultCloneURL = server.URL
	t.Cleanup(func() { DefaultCloneURL = oldCloneURL })

	result, err := engine.CloneVoice(context.Background(), app.VoiceCloneRequest{
		AppID:       "9193177346",
		AccessToken: "access-token",
		ResourceID:  "seed-icl-2.0",
		SpeakerID:   "S_z5F1jrL52",
		Language:    "ja",
		Samples: []app.VoiceCloneSample{
			{
				Filename:   "fairy.wav",
				MimeType:   "audio/wav",
				DataBase64: base64.StdEncoding.EncodeToString([]byte("fake wav")),
			},
		},
	})
	if err != nil {
		t.Fatalf("CloneVoice() error = %v", err)
	}
	if gotAppKey != "9193177346" {
		t.Fatalf("X-Api-App-Key = %q", gotAppKey)
	}
	if gotAccessKey != "access-token" {
		t.Fatalf("X-Api-Access-Key = %q", gotAccessKey)
	}
	if gotResourceID != "seed-icl-2.0" {
		t.Fatalf("X-Api-Resource-Id = %q", gotResourceID)
	}
	if gotBody.SpeakerID != "S_z5F1jrL52" {
		t.Fatalf("speaker_id = %q", gotBody.SpeakerID)
	}
	if gotBody.Language != 2 {
		t.Fatalf("language = %d, want ja code 2", gotBody.Language)
	}
	if gotBody.Audio.Format != "wav" {
		t.Fatalf("audio.format = %q", gotBody.Audio.Format)
	}
	if result.SampleCount != 1 {
		t.Fatalf("result.SampleCount = %d, want 1", result.SampleCount)
	}
	if result.Status != "training" {
		t.Fatalf("result.Status = %q", result.Status)
	}
}

func TestCloneVoiceRejectsMultipleSamplesForV3(t *testing.T) {
	t.Parallel()

	engine := NewEngine(Options{OutputDir: t.TempDir()})
	_, err := engine.CloneVoice(context.Background(), app.VoiceCloneRequest{
		AppID:       "9193177346",
		AccessToken: "access-token",
		ResourceID:  "seed-icl-2.0",
		SpeakerID:   "S_z5F1jrL52",
		Language:    "ja",
		Samples: []app.VoiceCloneSample{
			{Filename: "fairy-1.wav", DataBase64: base64.StdEncoding.EncodeToString([]byte("fake wav 1"))},
			{Filename: "fairy-2.wav", DataBase64: base64.StdEncoding.EncodeToString([]byte("fake wav 2"))},
		},
	})
	if err == nil {
		t.Fatal("CloneVoice() error = nil, want multiple sample error")
	}
	if !strings.Contains(err.Error(), "一次训练只接受 1 段 audio") {
		t.Fatalf("error = %q, want single audio hint", err.Error())
	}
}

func TestCloneVoiceRejectsStandardTTSResource(t *testing.T) {
	t.Parallel()

	engine := NewEngine(Options{OutputDir: t.TempDir()})
	_, err := engine.CloneVoice(context.Background(), app.VoiceCloneRequest{
		AppID:       "9193177346",
		AccessToken: "access-token",
		ResourceID:  "seed-tts-2.0",
		SpeakerID:   "S_z5F1jrL52",
		Language:    "ja",
		Samples: []app.VoiceCloneSample{
			{Filename: "fairy.wav", DataBase64: base64.StdEncoding.EncodeToString([]byte("fake wav"))},
		},
	})
	if err == nil {
		t.Fatal("CloneVoice() error = nil, want resource error")
	}
	if !strings.Contains(err.Error(), "seed-icl") {
		t.Fatalf("error = %q, want seed-icl hint", err.Error())
	}
}
