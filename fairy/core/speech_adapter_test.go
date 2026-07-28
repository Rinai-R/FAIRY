package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"fairy/config"
	"fairy/speech"
)

type readinessVoiceClient struct {
	calls int
}

func (c *readinessVoiceClient) TrainVoice(context.Context, speech.Settings, speech.Credentials, speech.TrainVoiceRequest) (speech.VoiceResult, error) {
	c.calls++
	return speech.VoiceResult{}, errors.New("unexpected provider call")
}

func (c *readinessVoiceClient) QueryVoice(context.Context, speech.Settings, speech.Credentials, speech.VoiceOperationRequest) (speech.VoiceResult, error) {
	c.calls++
	return speech.VoiceResult{}, errors.New("unexpected provider call")
}

func (c *readinessVoiceClient) UpgradeVoice(context.Context, speech.Settings, speech.Credentials, speech.VoiceOperationRequest) (speech.VoiceResult, error) {
	c.calls++
	return speech.VoiceResult{}, errors.New("unexpected provider call")
}

func (c *readinessVoiceClient) SynthesizeSpeech(context.Context, speech.Settings, speech.Credentials, speech.SynthesizeSpeechRequest) (speech.SynthesisResult, error) {
	c.calls++
	return speech.SynthesisResult{}, errors.New("unexpected provider call")
}

func TestCompanionSpeechAdapterSpeechReady(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(t *testing.T, root string, store *config.SecretStore)
		nilService bool
		wantReady  bool
		wantError  bool
	}{
		{
			name:       "nil service",
			nilService: true,
			wantError:  true,
		},
		{
			name: "disabled",
		},
		{
			name: "configured without default speaker",
			prepare: func(t *testing.T, root string, store *config.SecretStore) {
				t.Helper()
				if _, err := speech.SaveSettings(root, speech.SaveSettingsRequest{
					Enabled: true,
					APIKey:  "test-api-key",
				}, store); err != nil {
					t.Fatalf("SaveSettings() error = %v", err)
				}
			},
		},
		{
			name: "configured with default speaker",
			prepare: func(t *testing.T, root string, store *config.SecretStore) {
				t.Helper()
				if _, err := speech.SaveSettings(root, speech.SaveSettingsRequest{
					Enabled:        true,
					APIKey:         "test-api-key",
					DefaultSpeaker: "S_ready",
				}, store); err != nil {
					t.Fatalf("SaveSettings() error = %v", err)
				}
			},
			wantReady: true,
		},
		{
			name: "corrupt settings",
			prepare: func(t *testing.T, root string, _ *config.SecretStore) {
				t.Helper()
				dir := filepath.Join(root, "speech")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "volcengine_voice_clone_http.json"), []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &readinessVoiceClient{}
			adapter := companionSpeechAdapter{}
			if !test.nilService {
				root := t.TempDir()
				store := config.NewTestSecretStore()
				if test.prepare != nil {
					test.prepare(t, root, store)
				}
				adapter.service = speech.NewSpeechServiceWithClient(root, store, client)
			}

			ready, err := adapter.SpeechReady()
			if ready != test.wantReady || (err != nil) != test.wantError {
				t.Fatalf("SpeechReady() = (%v, %v), want (%v, error=%v)", ready, err, test.wantReady, test.wantError)
			}
			if client.calls != 0 {
				t.Fatalf("provider calls = %d, want 0", client.calls)
			}
		})
	}
}
