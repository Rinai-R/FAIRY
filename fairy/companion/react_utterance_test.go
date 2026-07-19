package companion

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"fairy/model"
)

// writeChatToolCallWithContent emits an assistant delta that carries a
// user-facing content line alongside a function tool call, mirroring a model
// that speaks a transition line while deciding to use a tool.
func writeChatToolCallWithContent(w http.ResponseWriter, content string, name string, argumentsJSON string) {
	encoded, err := json.Marshal(content)
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", encoded)
	payload := fmt.Sprintf(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":%q,"arguments":%q}}]},"finish_reason":null}]}`,
		name,
		argumentsJSON,
	)
	fmt.Fprintf(w, "data: %s\n\n", payload)
	fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
}

type recordedEvents struct {
	mu     sync.Mutex
	events []HarnessEvent
}

func (r *recordedEvents) add(event HarnessEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *recordedEvents) snapshot() []HarnessEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]HarnessEvent(nil), r.events...)
}

func utteranceEvents(events []HarnessEvent) []utterancePayload {
	out := make([]utterancePayload, 0)
	for _, event := range events {
		if payload, ok := event.Payload.(utterancePayload); ok {
			out = append(out, payload)
		}
	}
	return out
}

func beatReadyEvents(events []HarnessEvent) []beatReadyPayload {
	out := make([]beatReadyPayload, 0)
	for _, event := range events {
		if payload, ok := event.Payload.(beatReadyPayload); ok {
			out = append(out, payload)
		}
	}
	return out
}

func synthesizedEvents(events []HarnessEvent) []speechSynthesizedPayload {
	out := make([]speechSynthesizedPayload, 0)
	for _, event := range events {
		if payload, ok := event.Payload.(speechSynthesizedPayload); ok {
			out = append(out, payload)
		}
	}
	return out
}

func hasCompleted(events []HarnessEvent) bool {
	for _, event := range events {
		if _, ok := event.Payload.(completedPayload); ok {
			return true
		}
	}
	return false
}

func memoryToolThenReplyServer(t *testing.T, content string) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			if content == "" {
				writeChatToolCall(w, toolMemorySearch, `{"query":"安静"}`)
			} else {
				writeChatToolCallWithContent(w, content, toolMemorySearch, `{"query":"安静"}`)
			}
			return
		}
		writeChatTextDelta(w, testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "我记得你喜欢安静。"}))
		writeChatStop(w)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestReActUtteranceEmittedWithContentAndQueuesTTS(t *testing.T) {
	server := memoryToolThenReplyServer(t, "让我翻翻记忆哦")
	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
	synth := &fakeSpeechSynthesizer{result: SpeechSynthesisResult{SpeakerID: "spk", MimeType: "audio/mpeg", Format: "mp3", DataURL: "data:audio/mpeg;base64,AA"}}
	AttachSpeechSynthesizer(service, synth)
	recorder := &recordedEvents{}
	AttachEventEmitter(service, recorder.add)

	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "我今天想待着",
		SpeechEnabled:         true,
		MaxOutputTokens:       160,
		AvailableVisualStates: visualStates("idle"),
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn() error = %v", err)
	}
	if outcome.ResponseText != "我记得你喜欢安静。" {
		t.Fatalf("outcome = %#v", outcome)
	}
	events := recorder.snapshot()
	beats := beatReadyEvents(events)
	var utteranceBeat, finalBeat *beatReadyPayload
	for i := range beats {
		switch beats[i].Kind {
		case beatKindUtterance:
			utteranceBeat = &beats[i]
		case beatKindFinal:
			finalBeat = &beats[i]
		}
	}
	if utteranceBeat == nil || utteranceBeat.DisplayText != "让我翻翻记忆哦" {
		t.Fatalf("utterance beat = %#v", beats)
	}
	if utteranceBeat.Reason != "searching_memory" {
		t.Fatalf("utterance reason = %q", utteranceBeat.Reason)
	}
	if utteranceBeat.DataURL == "" || finalBeat == nil || finalBeat.DataURL == "" {
		t.Fatalf("expected paired audio on both beats, got %#v", beats)
	}
	if utteranceBeat.Index != 0 || finalBeat.Index != 1 {
		t.Fatalf("playback order wrong: utterance=%d final=%d", utteranceBeat.Index, finalBeat.Index)
	}
	if synth.calls.Load() != 2 {
		t.Fatalf("synth calls = %d, want 2 (utterance + chain)", synth.calls.Load())
	}
}

func TestSpeechSynthesizedEmittedBeforeCompleted(t *testing.T) {
	// The bubble uses `completed` as "no more audio coming". Regression guard:
	// every beat.ready must be emitted before completed so the frontend
	// never releases the hold (and flashes the bubble away) while audio is pending.
	server := memoryToolThenReplyServer(t, "让我翻翻记忆哦")
	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
	AttachSpeechSynthesizer(service, &fakeSpeechSynthesizer{result: SpeechSynthesisResult{SpeakerID: "spk", MimeType: "audio/mpeg", Format: "mp3", DataURL: "data:audio/mpeg;base64,AA"}})
	recorder := &recordedEvents{}
	AttachEventEmitter(service, recorder.add)

	if _, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "我今天想待着",
		SpeechEnabled:         true,
		MaxOutputTokens:       160,
		AvailableVisualStates: visualStates("idle"),
	}); err != nil {
		t.Fatalf("SubmitCompiledTurn() error = %v", err)
	}
	events := recorder.snapshot()
	completedAt := -1
	for i, event := range events {
		if _, ok := event.Payload.(completedPayload); ok {
			completedAt = i
		}
	}
	if completedAt < 0 {
		t.Fatal("no completed event")
	}
	beatCount := 0
	for i, event := range events {
		if _, ok := event.Payload.(beatReadyPayload); ok {
			beatCount++
			if i > completedAt {
				t.Fatalf("beat.ready at %d emitted after completed at %d", i, completedAt)
			}
		}
	}
	if beatCount != 2 {
		t.Fatalf("expected 2 beat.ready before completed, got %d", beatCount)
	}
}

func TestReActNoContentEmitsNoUtterance(t *testing.T) {
	server := memoryToolThenReplyServer(t, "")
	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
	recorder := &recordedEvents{}
	AttachEventEmitter(service, recorder.add)

	if _, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "我今天想待着",
		SpeechEnabled:         false,
		MaxOutputTokens:       160,
		AvailableVisualStates: visualStates("idle"),
	}); err != nil {
		t.Fatalf("SubmitCompiledTurn() error = %v", err)
	}
	if utterances := utteranceEvents(recorder.snapshot()); len(utterances) != 0 {
		t.Fatalf("expected no utterance, got %#v", utterances)
	}
}

func TestReActUtteranceTTSFailureDoesNotAbortTurn(t *testing.T) {
	server := memoryToolThenReplyServer(t, "让我翻翻记忆哦")
	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
	AttachSpeechSynthesizer(service, &fakeSpeechSynthesizer{err: fmt.Errorf("tts boom")})
	recorder := &recordedEvents{}
	AttachEventEmitter(service, recorder.add)

	outcome, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "我今天想待着",
		SpeechEnabled:         true,
		MaxOutputTokens:       160,
		AvailableVisualStates: visualStates("idle"),
	})
	if err != nil {
		t.Fatalf("SubmitCompiledTurn() error = %v (turn must not abort on TTS failure)", err)
	}
	if outcome.ResponseText != "我记得你喜欢安静。" {
		t.Fatalf("outcome = %#v", outcome)
	}
	events := recorder.snapshot()
	if !hasCompleted(events) {
		t.Fatal("turn must reach completed despite TTS failure")
	}
	if utterances := utteranceEvents(events); len(utterances) != 0 {
		t.Fatalf("legacy utterance must not be the delivery path, got %#v", utterances)
	}
	beats := beatReadyEvents(events)
	foundUtt := false
	for _, beat := range beats {
		if beat.Kind == beatKindUtterance && beat.DisplayText == "让我翻翻记忆哦" {
			foundUtt = true
			if beat.DataURL != "" {
				t.Fatalf("TTS failure should deliver text-only beat, got %#v", beat)
			}
		}
	}
	if !foundUtt {
		t.Fatalf("utterance text must still show via beat.ready, got %#v", beats)
	}
}

func TestReActSpeechDisabledMakesNoTTSRequest(t *testing.T) {
	server := memoryToolThenReplyServer(t, "让我翻翻记忆哦")
	root := t.TempDir()
	writeModelConnectionWithEndpoint(t, root, "chat_completions", server.URL, "no_auth")
	memoryStore, characterID := seedCompanionRuntime(t, root)
	bootstrap, err := memoryStore.OpenOrCreateCharacterConversation(characterID)
	if err != nil {
		t.Fatalf("OpenOrCreateCharacterConversation() error = %v", err)
	}
	service := NewCompanionServiceWithRuntime(root, memoryStore, model.NewModelServiceWithTransport(root, model.SDKTransport{HTTPClient: server.Client()}, nil), nil)
	synth := &fakeSpeechSynthesizer{result: SpeechSynthesisResult{SpeakerID: "spk", MimeType: "audio/mpeg", Format: "mp3", DataURL: "data:audio/mpeg;base64,AA"}}
	AttachSpeechSynthesizer(service, synth)
	recorder := &recordedEvents{}
	AttachEventEmitter(service, recorder.add)

	if _, err := service.SubmitCompiledTurn(SubmitCompiledTurnRequest{
		ConversationID:        bootstrap.Conversation.ID,
		Input:                 "我今天想待着",
		SpeechEnabled:         false,
		MaxOutputTokens:       160,
		AvailableVisualStates: visualStates("idle"),
	}); err != nil {
		t.Fatalf("SubmitCompiledTurn() error = %v", err)
	}
	events := recorder.snapshot()
	if synth.calls.Load() != 0 {
		t.Fatalf("speech disabled but synth called %d times", synth.calls.Load())
	}
	beats := beatReadyEvents(events)
	foundUtt := false
	for _, beat := range beats {
		if beat.Kind == beatKindUtterance && beat.DisplayText == "让我翻翻记忆哦" {
			foundUtt = true
		}
	}
	if !foundUtt {
		t.Fatalf("utterance text must still show via beat.ready, got %#v", beats)
	}
	if len(synthesizedEvents(events)) != 0 {
		t.Fatal("no speech.synthesized expected when speech disabled")
	}
}
