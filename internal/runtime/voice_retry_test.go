package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Rinai-R/FAIRY/internal/adapters/voice"
	"github.com/Rinai-R/FAIRY/internal/app"
)

func TestSynthesizeVoiceWithRetryRetriesVolcengineConcurrencyQuota(t *testing.T) {
	oldBackoffs := voiceSynthesisRetryBackoffs
	oldSleep := voiceSynthesisSleep
	t.Cleanup(func() {
		voiceSynthesisRetryBackoffs = oldBackoffs
		voiceSynthesisSleep = oldSleep
	})
	voiceSynthesisRetryBackoffs = []time.Duration{time.Millisecond, time.Millisecond}
	sleeps := 0
	voiceSynthesisSleep = func(context.Context, time.Duration) error {
		sleeps++
		return nil
	}

	calls := 0
	audio, err := synthesizeVoiceWithRetry(context.Background(), func(context.Context) (app.AudioResult, error) {
		calls++
		if calls < 3 {
			return app.AudioResult{}, errors.New("volcengine v3 tts 解析失败 logid=2026062016500979A131F908CD3B46489D: code=45000292 message=quota exceeded for types: concurrency/")
		}
		return app.AudioResult{URL: "/audio/ok.mp3", Format: "mp3"}, nil
	})
	if err != nil {
		t.Fatalf("synthesizeVoiceWithRetry() error = %v", err)
	}
	if audio.URL != "/audio/ok.mp3" {
		t.Fatalf("audio.URL = %q, want /audio/ok.mp3", audio.URL)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if sleeps != 2 {
		t.Fatalf("sleeps = %d, want 2", sleeps)
	}
}

func TestSynthesizeVoiceWithRetryDoesNotRetryPermanentError(t *testing.T) {
	oldBackoffs := voiceSynthesisRetryBackoffs
	oldSleep := voiceSynthesisSleep
	t.Cleanup(func() {
		voiceSynthesisRetryBackoffs = oldBackoffs
		voiceSynthesisSleep = oldSleep
	})
	voiceSynthesisRetryBackoffs = []time.Duration{time.Millisecond}
	voiceSynthesisSleep = func(context.Context, time.Duration) error {
		t.Fatal("voiceSynthesisSleep() should not be called for permanent errors")
		return nil
	}

	calls := 0
	_, err := synthesizeVoiceWithRetry(context.Background(), func(context.Context) (app.AudioResult, error) {
		calls++
		return app.AudioResult{}, errors.New("volcengine speaker 不能为空")
	})
	if err == nil {
		t.Fatal("synthesizeVoiceWithRetry() error = nil, want permanent error")
	}
	if !strings.Contains(err.Error(), "speaker") {
		t.Fatalf("error = %v, want speaker error", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if got := voiceSynthesisRetryCount(err); got != 0 {
		t.Fatalf("retry count = %d, want 0", got)
	}
}

func TestSynthesizeVoiceWithRetryReportsRetryCountOnFinalFailure(t *testing.T) {
	oldBackoffs := voiceSynthesisRetryBackoffs
	oldSleep := voiceSynthesisSleep
	t.Cleanup(func() {
		voiceSynthesisRetryBackoffs = oldBackoffs
		voiceSynthesisSleep = oldSleep
	})
	voiceSynthesisRetryBackoffs = []time.Duration{time.Millisecond, time.Millisecond}
	sleeps := 0
	voiceSynthesisSleep = func(context.Context, time.Duration) error {
		sleeps++
		return nil
	}

	calls := 0
	_, err := synthesizeVoiceWithRetry(context.Background(), func(context.Context) (app.AudioResult, error) {
		calls++
		return app.AudioResult{}, errors.New("volcengine v3 tts 解析失败 logid=2026062016500979A131F908CD3B46489D: code=45000292 message=quota exceeded for types: concurrency/")
	})
	if err == nil {
		t.Fatal("synthesizeVoiceWithRetry() error = nil, want quota error")
	}
	if got := voiceSynthesisRetryCount(err); got != 2 {
		t.Fatalf("retry count = %d, want 2", got)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if sleeps != 2 {
		t.Fatalf("sleeps = %d, want 2", sleeps)
	}
}

func TestSynthesizeVoiceRuntimeEventIncludesRetryCount(t *testing.T) {
	oldBackoffs := voiceSynthesisRetryBackoffs
	oldSleep := voiceSynthesisSleep
	t.Cleanup(func() {
		voiceSynthesisRetryBackoffs = oldBackoffs
		voiceSynthesisSleep = oldSleep
	})
	voiceSynthesisRetryBackoffs = []time.Duration{time.Millisecond, time.Millisecond}
	voiceSynthesisSleep = func(context.Context, time.Duration) error { return nil }

	provider := voice.Provider("test-retry-voice")
	store := NewFileSessionStore(t.TempDir() + "/sessions.json")
	if _, err := store.BeginScene(app.SceneGenerateRequest{
		Topic:      "语音并发限制",
		Characters: []app.Character{{ID: "atri", DisplayName: "亚托莉"}},
	}, app.SceneGenerateResponse{
		Scene:   app.Scene{ID: "lesson", Title: "课程"},
		Session: app.Session{ID: "lesson:voice-quota", UserID: "default", ActiveCharacterID: "atri"},
		Workflow: app.TeachingWorkflow{
			ID:            "wf",
			CurrentNodeID: "opening",
			Nodes: []app.TeachingWorkflowNode{{
				ID:     "opening",
				Kind:   "opening",
				Title:  "开场",
				Status: app.WorkflowNodeStatusReady,
			}},
		},
	}); err != nil {
		t.Fatalf("BeginScene() error = %v", err)
	}
	rt := NewRuntime(Dependencies{
		Voices: map[voice.Provider]voice.Engine{
			provider: retryFailingVoiceEngine{err: errors.New("volcengine v3 tts 解析失败 logid=2026062016500979A131F908CD3B46489D: code=45000292 message=quota exceeded for types: concurrency/")},
		},
		DefaultVoice: provider,
		Sessions:     store,
		Logger:       slog.Default(),
	})
	_, err := rt.SynthesizeVoice(context.Background(), app.VoiceSynthesisRequest{
		Provider:       string(provider),
		Text:           "こんにちは",
		SessionID:      "lesson:voice-quota",
		WorkflowNodeID: "opening",
		Character:      app.Character{ID: "atri", DisplayName: "亚托莉"},
		Plan:           app.VoicePlan{VoiceID: "atri"},
	})
	if err == nil {
		t.Fatal("SynthesizeVoice() error = nil, want quota error")
	}
	record, err := store.Get("lesson:voice-quota")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	event := runtimeEventByTypeForRetryTest(record.Events, app.RuntimeEventTypeVoiceSynthesizeFailed)
	if event.ID == "" {
		t.Fatalf("voice.synthesize.failed event missing: %+v", record.Events)
	}
	if event.RetryCount != 2 {
		t.Fatalf("retry count = %d, want 2; event = %+v", event.RetryCount, event)
	}
	if event.DurationMS <= 0 {
		t.Fatalf("voice.synthesize.failed duration_ms = %d, want > 0", event.DurationMS)
	}
}

type retryFailingVoiceEngine struct {
	err error
}

func (e retryFailingVoiceEngine) Synthesize(context.Context, voice.Input) (app.AudioResult, error) {
	return app.AudioResult{}, e.err
}

func runtimeEventByTypeForRetryTest(events []app.RuntimeEvent, eventType string) app.RuntimeEvent {
	for _, event := range events {
		if event.Type == eventType {
			return event
		}
	}
	return app.RuntimeEvent{}
}
