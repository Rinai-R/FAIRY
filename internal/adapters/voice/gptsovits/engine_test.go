package gptsovits

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app"
)

func TestNormalizeEndpointAcceptsLocalForms(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "http://127.0.0.1:9880"},
		{name: "host port", in: "127.0.0.1:9880", want: "http://127.0.0.1:9880"},
		{name: "http base", in: "http://127.0.0.1:9880", want: "http://127.0.0.1:9880"},
		{name: "http tts", in: "http://127.0.0.1:9880/tts", want: "http://127.0.0.1:9880"},
		{name: "https tts slash", in: "https://voice.example.test/tts/", want: "https://voice.example.test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeEndpoint(tt.in); got != tt.want {
				t.Fatalf("normalizeEndpoint(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSynthesizeUsesServiceLevelPayloadAndNormalizedEndpoint(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("content-type", "audio/mpeg")
		_, _ = w.Write([]byte("audio"))
	}))
	defer server.Close()

	engine := NewEngine(Options{
		Endpoint:        "127.0.0.1:1",
		RefAudioPath:    "/env/ref.wav",
		PromptText:      "env reference",
		TextLang:        "zh",
		PromptLang:      "zh",
		MediaType:       "wav",
		TextSplitMethod: "cut0",
		OutputDir:       t.TempDir(),
	})
	result, err := engine.Synthesize(context.Background(), voice.Input{
		Text: "こんにちは。",
		Profile: app.VoiceProfile{
			Endpoint:        strings.TrimPrefix(server.URL, "http://") + "/tts",
			RefAudioPath:    "/uploaded/ref.wav",
			PromptText:      "こんにちは。",
			TextLang:        "ja",
			PromptLang:      "ja",
			MediaType:       "mp3",
			TextSplitMethod: "cut5",
			Extra: map[string]string{
				"ref_audio_path":    "/role/ref.wav",
				"prompt_text":       "role reference",
				"prompt_lang":       "en",
				"text_lang":         "en",
				"text_split_method": "cut1",
				"media_type":        "ogg",
			},
		},
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if gotPath != "/tts" {
		t.Fatalf("request path = %q, want /tts", gotPath)
	}
	if result.Placeholder || result.URL == "" || result.Format != "mp3" {
		t.Fatalf("audio result = %#v", result)
	}
	want := map[string]any{
		"text":              "こんにちは。",
		"ref_audio_path":    "/uploaded/ref.wav",
		"prompt_text":       "こんにちは。",
		"text_lang":         "ja",
		"prompt_lang":       "ja",
		"text_split_method": "cut5",
		"media_type":        "mp3",
		"batch_size":        float64(1),
		"streaming_mode":    false,
	}
	for key, value := range want {
		if gotBody[key] != value {
			t.Fatalf("payload[%s] = %#v, want %#v; body=%#v", key, gotBody[key], value, gotBody)
		}
	}
	for key, value := range map[string]any{
		"ref_audio_path":    "/role/ref.wav",
		"prompt_text":       "role reference",
		"text_lang":         "en",
		"prompt_lang":       "en",
		"text_split_method": "cut1",
		"media_type":        "ogg",
	} {
		if gotBody[key] == value {
			t.Fatalf("payload includes role-level extra override %s=%#v: %#v", key, value, gotBody)
		}
	}
}

func TestSynthesizeRequiresReferenceAudioPath(t *testing.T) {
	engine := NewEngine(Options{OutputDir: t.TempDir()})
	_, err := engine.Synthesize(context.Background(), voice.Input{Text: "hello"})
	if err == nil {
		t.Fatal("Synthesize() error = nil, want missing ref_audio_path error")
	}
	if !strings.Contains(err.Error(), "ref_audio_path") {
		t.Fatalf("error = %q, want ref_audio_path context", err)
	}
}

func TestSynthesizeConvertsExistingRelativeReferenceAudioPath(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("content-type", "audio/wav")
		_, _ = w.Write([]byte("audio"))
	}))
	defer server.Close()

	workDir := t.TempDir()
	oldWorkDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWorkDir)
	})
	if err := os.MkdirAll(filepath.Join(workDir, "data", "materials"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	relativeRef := filepath.Join("data", "materials", "ref.wav")
	absoluteRef := filepath.Join(workDir, relativeRef)
	if err := os.WriteFile(absoluteRef, []byte("ref"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	expectedRef, err := filepath.Abs(relativeRef)
	if err != nil {
		t.Fatalf("Abs() error = %v", err)
	}

	engine := NewEngine(Options{
		Endpoint:  server.URL,
		OutputDir: t.TempDir(),
	})
	_, err = engine.Synthesize(context.Background(), voice.Input{
		Text: "こんにちは。",
		Profile: app.VoiceProfile{
			RefAudioPath: relativeRef,
		},
	})
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if gotBody["ref_audio_path"] != expectedRef {
		t.Fatalf("ref_audio_path = %#v, want %q; body=%#v", gotBody["ref_audio_path"], expectedRef, gotBody)
	}
}
