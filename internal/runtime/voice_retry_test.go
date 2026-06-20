package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
}
