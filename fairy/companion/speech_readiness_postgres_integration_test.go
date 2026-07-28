//go:build integration

package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"fairy/model"
	"fairy/session"
)

type speechReadinessIntegrationModel struct {
	mu             sync.Mutex
	respondCalls   int
	translateCalls int
	translateError error
}

func (m *speechReadinessIntegrationModel) ExecuteRequestContext(_ context.Context, request model.CompiledPromptRequest) ([]model.StreamEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch request.Shape.Lane {
	case model.PromptLaneRespond:
		m.respondCalls++
		return []model.StreamEvent{
			{Type: "text_delta", Data: `{"chains":[{"visualState":"idle","text":"今天见。"}]}`},
			{Type: "usage", Usage: &model.Usage{PromptTokens: 10, CompletionTokens: 4}},
		}, nil
	case model.PromptLaneTranslate:
		m.translateCalls++
		if m.translateError != nil {
			return nil, m.translateError
		}
		return []model.StreamEvent{
			{Type: "text_delta", Data: "また今日。"},
			{Type: "usage", Usage: &model.Usage{PromptTokens: 5, CompletionTokens: 3}},
		}, nil
	default:
		return nil, fmt.Errorf("unexpected model lane %q", request.Shape.Lane)
	}
}

func (*speechReadinessIntegrationModel) ExecutePrompt(model.PromptLane, string, uint32, []model.PromptItem, string) ([]model.StreamEvent, error) {
	return nil, errors.New("unexpected ExecutePrompt")
}

func (m *speechReadinessIntegrationModel) calls() (respond int, translate int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.respondCalls, m.translateCalls
}

type speechReadinessIntegrationRuntime struct {
	mu             sync.Mutex
	ready          bool
	readinessError error
	readinessCalls int
	synthesisCalls int
	synthesisTexts []string
}

func (r *speechReadinessIntegrationRuntime) SpeechReady() (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readinessCalls++
	return r.ready, r.readinessError
}

func (r *speechReadinessIntegrationRuntime) SynthesizeSpeech(request SpeechSynthesisRequest) (SpeechSynthesisResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.synthesisCalls++
	r.synthesisTexts = append(r.synthesisTexts, request.Text)
	return SpeechSynthesisResult{
		SpeakerID: "S_fake",
		MimeType:  "audio/mpeg",
		Format:    "mp3",
		DataURL:   "data:audio/mpeg;base64,ZmFrZQ==",
	}, nil
}

func (r *speechReadinessIntegrationRuntime) calls() (readiness int, synthesis int, texts []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readinessCalls, r.synthesisCalls, append([]string(nil), r.synthesisTexts...)
}

func TestPostgresSpeechReadinessGatesTranslationAndSynthesisIntegration(t *testing.T) {
	readinessFixture := "sensitive-readiness-fixture"
	translateFixture := "translate unavailable"
	tests := []struct {
		name                string
		speechRequested     bool
		ready               bool
		readinessError      error
		translateError      error
		wantReadinessCalls  int
		wantTranslateCalls  int
		wantSynthesisCalls  int
		wantSpeechRequested bool
		wantSpeechText      string
		wantAudio           bool
		wantReadinessLedger int
	}{
		{
			name:            "surface did not request speech",
			ready:           true,
			speechRequested: false,
		},
		{
			name:               "core speech not ready",
			speechRequested:    true,
			wantReadinessCalls: 1,
		},
		{
			name:                "readiness read failed",
			speechRequested:     true,
			ready:               true,
			readinessError:      errors.New(readinessFixture),
			wantReadinessCalls:  1,
			wantReadinessLedger: 1,
		},
		{
			name:                "core speech ready",
			speechRequested:     true,
			ready:               true,
			wantReadinessCalls:  1,
			wantTranslateCalls:  1,
			wantSynthesisCalls:  1,
			wantSpeechRequested: true,
			wantSpeechText:      "また今日。",
			wantAudio:           true,
		},
		{
			name:                "translate failed after readiness",
			speechRequested:     true,
			ready:               true,
			translateError:      errors.New(translateFixture),
			wantReadinessCalls:  1,
			wantTranslateCalls:  1,
			wantSpeechRequested: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, _, cleanup := openCompanionIntegrationStore(t)
			defer cleanup()
			characterID := "character-speech-readiness-" + strings.ReplaceAll(test.name, " ", "-")
			bootstrap, err := store.OpenOrCreateCharacterConversation(characterID)
			if err != nil {
				t.Fatal(err)
			}
			provider := &speechReadinessIntegrationModel{translateError: test.translateError}
			service := newCompanionIntegrationService(store, characterID, provider)
			record := companionIntegrationCharacter(characterID)
			record.TextLanguage = "zh"
			record.SpeakingLanguage = "ja"
			AttachCharacterLookup(service, companionIntegrationCharacterLookup{record: record})
			mustBindDesktopInteraction(t, service, bootstrap.Conversation.ID)
			runtime := &speechReadinessIntegrationRuntime{
				ready:          test.ready,
				readinessError: test.readinessError,
			}
			AttachSpeechRuntime(service, runtime)
			var (
				eventMu sync.Mutex
				events  []session.Event
			)
			AttachEventEmitter(service, func(event session.Event) {
				eventMu.Lock()
				events = append(events, event)
				eventMu.Unlock()
			})

			outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
				ConversationID:        bootstrap.Conversation.ID,
				Input:                 "今天见",
				SpeechEnabled:         test.speechRequested,
				MaxOutputTokens:       160,
				AvailableVisualStates: []VisualState{{ID: "idle", Description: "idle"}},
			})
			if err != nil {
				t.Fatalf("SubmitCompiledTurn() error = %v", err)
			}
			if outcome.ResponseText != "今天见。" ||
				outcome.SpeechText != test.wantSpeechText ||
				outcome.SpeechRequested != test.wantSpeechRequested {
				t.Fatalf("outcome = %#v", outcome)
			}

			readinessCalls, synthesisCalls, synthesisTexts := runtime.calls()
			respondCalls, translateCalls := provider.calls()
			if readinessCalls != test.wantReadinessCalls ||
				respondCalls != 1 ||
				translateCalls != test.wantTranslateCalls ||
				synthesisCalls != test.wantSynthesisCalls {
				t.Fatalf(
					"calls readiness/respond/translate/synthesis = %d/%d/%d/%d, want %d/1/%d/%d",
					readinessCalls,
					respondCalls,
					translateCalls,
					synthesisCalls,
					test.wantReadinessCalls,
					test.wantTranslateCalls,
					test.wantSynthesisCalls,
				)
			}
			if test.wantSynthesisCalls == 1 &&
				(len(synthesisTexts) != 1 || synthesisTexts[0] != test.wantSpeechText) {
				t.Fatalf("synthesis texts = %#v, want [%q]", synthesisTexts, test.wantSpeechText)
			}

			eventMu.Lock()
			finalBeats := finalBeatEvents(events)
			eventMu.Unlock()
			if len(finalBeats) != 1 {
				t.Fatalf("final beats = %#v", finalBeats)
			}
			beat := finalBeats[0]
			hasAudio := beat.DataURL != ""
			if beat.DisplayText != "今天见。" ||
				beat.SpeechText != test.wantSpeechText ||
				hasAudio != test.wantAudio {
				t.Fatalf("final beat = %#v", beat)
			}

			reloaded, err := store.LoadConversation(bootstrap.Conversation.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(reloaded.Messages) != 2 ||
				reloaded.Messages[1].Role != "assistant" ||
				reloaded.Messages[1].Content != "今天见。" {
				t.Fatalf("messages = %#v", reloaded.Messages)
			}

			ledger, err := store.ListTurnRuntimeEvents(outcome.ConversationID, outcome.TurnID)
			if err != nil {
				t.Fatal(err)
			}
			readinessLedger := 0
			for _, event := range ledger {
				if strings.Contains(event.MetadataJSON, readinessFixture) {
					t.Fatalf("runtime ledger leaked readiness error: %s", event.MetadataJSON)
				}
				if event.EventType != runtimeLedgerEventSpeech {
					continue
				}
				var metadata struct {
					Reason string `json:"reason"`
				}
				if json.Unmarshal([]byte(event.MetadataJSON), &metadata) == nil &&
					metadata.Reason == "speech_readiness_failed" {
					readinessLedger++
				}
			}
			if readinessLedger != test.wantReadinessLedger {
				t.Fatalf("speech readiness ledger events = %d, want %d", readinessLedger, test.wantReadinessLedger)
			}
		})
	}
}
