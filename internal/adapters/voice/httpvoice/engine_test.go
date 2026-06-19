package httpvoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app"
)

func TestSynthesizeUsesStandardVoiceServiceContract(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "audio/wav")
		_, _ = w.Write([]byte("voice-audio"))
	}))
	defer server.Close()

	outputDir := t.TempDir()
	engine := NewEngine(Options{
		Provider:     "voice-service",
		Endpoint:     server.URL,
		OutputDir:    outputDir,
		BaseURL:      "/audio/",
		OutputFormat: "mp3",
	})
	result, err := engine.Synthesize(context.Background(), voice.Input{
		Text: "こんにちは。",
		Plan: app.VoicePlan{VoiceID: "plan-voice", Style: "gentle", Speed: 1.1, Pitch: 0.9},
		Profile: app.VoiceProfile{
			VoiceID:   "atri",
			TextLang:  "ja",
			MediaType: "wav",
		},
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if gotPath != "/v1/synthesize" {
		t.Fatalf("path = %q, want /v1/synthesize", gotPath)
	}
	want := map[string]any{
		"voice_id": "atri",
		"text":     "こんにちは。",
		"language": "ja",
		"format":   "wav",
		"style":    "gentle",
		"speed":    float64(1.1),
		"pitch":    float64(0.9),
	}
	for key, value := range want {
		if gotBody[key] != value {
			t.Fatalf("request[%s] = %#v, want %#v; body=%#v", key, gotBody[key], value, gotBody)
		}
	}
	if !strings.HasPrefix(result.URL, "/audio/") || result.Format != "wav" || result.Placeholder {
		t.Fatalf("result = %#v", result)
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("written files = %d, want 1", len(entries))
	}
	raw, err := os.ReadFile(outputDir + "/" + entries[0].Name())
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(raw) != "voice-audio" {
		t.Fatalf("written audio = %q", raw)
	}
}

func TestSynthesizeAcceptsJSONAudioURLCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"audio_url":"http://voice.test/audio/atri.wav","format":"wav","duration_ms":1200}`))
	}))
	defer server.Close()

	engine := NewEngine(Options{
		Provider:     "voice-service",
		Endpoint:     server.URL,
		OutputFormat: "mp3",
	})
	result, err := engine.Synthesize(context.Background(), voice.Input{Text: "こんにちは。"})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if result.URL != "http://voice.test/audio/atri.wav" || result.Format != "wav" || result.DurationMS != 1200 || result.Placeholder {
		t.Fatalf("result = %#v", result)
	}
}

func TestSynthesizeNormalizesHostPortEndpoint(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("content-type", "audio/wav")
		_, _ = w.Write([]byte("audio"))
	}))
	defer server.Close()

	outputDir := t.TempDir()
	engine := NewEngine(Options{
		Provider:  "voice-service",
		Endpoint:  strings.TrimPrefix(server.URL, "http://"),
		OutputDir: outputDir,
	})
	if _, err := engine.Synthesize(context.Background(), voice.Input{Text: "hello"}); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if gotPath != "/v1/synthesize" {
		t.Fatalf("path = %q, want /v1/synthesize", gotPath)
	}
}

func TestSynthesizeDoesNotAppendPathTwice(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("content-type", "audio/wav")
		_, _ = w.Write([]byte("audio"))
	}))
	defer server.Close()

	outputDir := t.TempDir()
	engine := NewEngine(Options{
		Provider:  "voice-service",
		Endpoint:  server.URL + "/v1/synthesize",
		OutputDir: outputDir,
	})
	if _, err := engine.Synthesize(context.Background(), voice.Input{Text: "hello"}); err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if gotPath != "/v1/synthesize" {
		t.Fatalf("path = %q, want /v1/synthesize", gotPath)
	}
}

func TestStandardContractDoesNotInheritCharacterVoiceID(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "audio/wav")
		_, _ = w.Write([]byte("audio"))
	}))
	defer server.Close()

	engine := NewEngine(Options{
		Provider:  "voice-service",
		Endpoint:  server.URL,
		OutputDir: t.TempDir(),
	})
	_, err := engine.Synthesize(context.Background(), voice.Input{
		Text:      "hello",
		Plan:      app.VoicePlan{VoiceID: "plan-voice"},
		Character: app.Character{VoiceID: "character-voice"},
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if _, ok := gotBody["voice_id"]; ok {
		t.Fatalf("standard request inherited voice_id unexpectedly: %#v", gotBody)
	}
}

func TestSynthesizeRejectsStandardJSONWithoutAudioURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"format":"wav"}`))
	}))
	defer server.Close()

	engine := NewEngine(Options{Provider: "voice-service", Endpoint: server.URL})
	_, err := engine.Synthesize(context.Background(), voice.Input{Text: "hello"})
	if err == nil {
		t.Fatal("Synthesize() error = nil, want missing audio_url error")
	}
	if !strings.Contains(err.Error(), "audio_url") {
		t.Fatalf("error = %q, want audio_url context", err)
	}
}

func TestEndpointHintExplainsRawLocalTTSEndpointMistake(t *testing.T) {
	hint := endpointHint("voice-service", "127.0.0.1:9880", http.StatusNotFound)
	if !strings.Contains(hint, "默认 http://127.0.0.1:8791") {
		t.Fatalf("hint = %q, want gateway endpoint context", hint)
	}
}

func TestSynthesizeKeepsTemplateBinaryCompatibility(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("content-type", "audio/wav")
		_, _ = w.Write([]byte("audio"))
	}))
	defer server.Close()

	outputDir := t.TempDir()
	engine := NewEngine(Options{
		Provider:     "legacy-http",
		Endpoint:     server.URL,
		Path:         "/tts",
		BodyTemplate: `{"text":"{{text}}","voice":"{{voice_id}}"}`,
		OutputDir:    outputDir,
		BaseURL:      "/audio/",
		OutputFormat: "wav",
	})
	result, err := engine.Synthesize(context.Background(), voice.Input{
		Text:    "hello",
		Profile: app.VoiceProfile{VoiceID: "legacy"},
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if gotBody["text"] != "hello" || gotBody["voice"] != "legacy" {
		t.Fatalf("request body = %#v", gotBody)
	}
	if result.URL == "" || result.Format != "wav" || result.Placeholder {
		t.Fatalf("result = %#v", result)
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("written files = %d, want 1", len(entries))
	}
}
