package companion

import (
	"encoding/json"
	"testing"
)

func TestSpeechSynthesizedCarriesChainIndexAndPlaybackIndex(t *testing.T) {
	life := NewTurnLifecycle("conversation-1", "turn-1")
	if _, err := life.Transition(TurnStateInterpreting); err != nil {
		t.Fatalf("interpreting: %v", err)
	}
	if _, err := life.Transition(TurnStateGathering); err != nil {
		t.Fatalf("gathering: %v", err)
	}
	if _, err := life.Transition(TurnStatePlanning); err != nil {
		t.Fatalf("planning: %v", err)
	}

	// Utterance audio: playback index 0, chainIndex -1.
	event, err := life.SpeechSynthesized(SpeechSynthesisCompletion{
		Index:      0,
		ChainIndex: chainIndexUtterance,
		Text:       "让我看看",
		Result:     SpeechSynthesisResult{SpeakerID: "spk", MimeType: "audio/mpeg", Format: "mp3", DataURL: "data:audio/mpeg;base64,AA"},
	})
	if err != nil {
		t.Fatalf("SpeechSynthesized() error = %v", err)
	}
	encoded, err := json.Marshal(event.Payload)
	if err != nil {
		t.Fatalf("marshal error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal error = %v", err)
	}
	if decoded["type"] != "speech.synthesized" {
		t.Fatalf("type = %v", decoded["type"])
	}
	if decoded["index"].(float64) != 0 {
		t.Fatalf("index = %v, want 0 (playback order)", decoded["index"])
	}
	if decoded["chainIndex"].(float64) != -1 {
		t.Fatalf("chainIndex = %v, want -1 (utterance)", decoded["chainIndex"])
	}
}
