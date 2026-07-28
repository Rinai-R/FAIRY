package companion

import (
	"errors"
	"testing"
)

type readinessSpeechRuntime struct {
	ready          bool
	readinessError error
	readinessCalls int
	synthesisCalls int
}

func (r *readinessSpeechRuntime) SpeechReady() (bool, error) {
	r.readinessCalls++
	return r.ready, r.readinessError
}

func (r *readinessSpeechRuntime) SynthesizeSpeech(SpeechSynthesisRequest) (SpeechSynthesisResult, error) {
	r.synthesisCalls++
	return SpeechSynthesisResult{}, nil
}

func TestSpeechEnabledForTurn(t *testing.T) {
	readinessError := errors.New("readiness unavailable")
	tests := []struct {
		name               string
		requested          bool
		runtime            *readinessSpeechRuntime
		wantEnabled        bool
		wantError          bool
		wantReadinessCalls int
	}{
		{
			name:      "surface did not request speech",
			runtime:   &readinessSpeechRuntime{ready: true},
			requested: false,
		},
		{
			name:      "runtime unavailable",
			requested: true,
		},
		{
			name:               "runtime not ready",
			requested:          true,
			runtime:            &readinessSpeechRuntime{},
			wantReadinessCalls: 1,
		},
		{
			name:               "readiness error",
			requested:          true,
			runtime:            &readinessSpeechRuntime{ready: true, readinessError: readinessError},
			wantError:          true,
			wantReadinessCalls: 1,
		},
		{
			name:               "runtime ready",
			requested:          true,
			runtime:            &readinessSpeechRuntime{ready: true},
			wantEnabled:        true,
			wantReadinessCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewCompanionService()
			if test.runtime != nil {
				AttachSpeechRuntime(service, test.runtime)
			}
			enabled, err := service.speechEnabledForTurn(test.requested)
			if enabled != test.wantEnabled || (err != nil) != test.wantError {
				t.Fatalf("speechEnabledForTurn() = (%v, %v), want (%v, error=%v)", enabled, err, test.wantEnabled, test.wantError)
			}
			if test.runtime != nil {
				if test.runtime.readinessCalls != test.wantReadinessCalls {
					t.Fatalf("readiness calls = %d, want %d", test.runtime.readinessCalls, test.wantReadinessCalls)
				}
				if test.runtime.synthesisCalls != 0 {
					t.Fatalf("synthesis calls = %d, want 0", test.runtime.synthesisCalls)
				}
			}
		})
	}
}
