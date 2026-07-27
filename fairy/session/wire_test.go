package session

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestOpenRequestJSONContract(t *testing.T) {
	request := OpenRequest{
		Endpoint:    EndpointIM,
		EndpointKey: "onebot-group:123",
		Interaction: Context{
			Audience:     AudienceMulti,
			Initiation:   InitiationAmbient,
			Presentation: PresentationChat,
		},
		OutputCapabilities: OutputCapabilities{Sticker: true},
	}

	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"endpoint":"im","endpointKey":"onebot-group:123","interaction":{"audience":"multi","initiation":"ambient","presentation":"chat"},"outputCapabilities":{"sticker":true}}`
	if string(raw) != want {
		t.Fatalf("OpenRequest JSON = %s, want %s", raw, want)
	}

	var roundTrip OpenRequest
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roundTrip, request) {
		t.Fatalf("OpenRequest round trip = %#v, want %#v", roundTrip, request)
	}
}

func TestOpenRequestMissingOutputCapabilitiesDefaultsToUnsupported(t *testing.T) {
	const raw = `{"endpoint":"im","endpointKey":"onebot-group:123","interaction":{"audience":"multi","initiation":"ambient","presentation":"chat"}}`
	var request OpenRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	if request.OutputCapabilities.Sticker {
		t.Fatal("missing sticker output capability must default to false")
	}
}

func TestExpressionDeliveryResultValidation(t *testing.T) {
	valid := ExpressionDeliveryResult{
		ConversationID: "conversation-1", TurnID: "turn-1", BeatID: "final-0",
		Status: ExpressionDeliverySucceeded,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.Status = ExpressionDeliveryFailed
	valid.ErrorMessage = "image send failed"
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.ErrorMessage = ""
	if err := valid.Validate(); err == nil {
		t.Fatal("failed delivery without error accepted")
	}
}

func TestMessageRecordExpressionPartsJSONContract(t *testing.T) {
	record := MessageRecord{
		ID: "message-1", ConversationID: "conversation-1", TurnID: "turn-1",
		Sequence: 2, Role: "assistant", Content: "",
		Parts: []ExpressionPart{{
			Kind: ExpressionSticker, VisualState: "surprised",
			Sticker: &StickerReference{ID: "sticker-1", Description: "震惊和无语", MIMEType: "image/webp"},
		}},
		CreatedAtUnixMS: 123,
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"id":"message-1","conversationId":"conversation-1","turnId":"turn-1","sequence":2,"role":"assistant","content":"","parts":[{"kind":"sticker","visualState":"surprised","sticker":{"id":"sticker-1","description":"震惊和无语","mimeType":"image/webp"}}],"createdAtUnixMs":123}`
	if string(raw) != want {
		t.Fatalf("MessageRecord JSON = %s, want %s", raw, want)
	}
}

func TestTurnEventJSONContract(t *testing.T) {
	event := Event{
		ConversationID: "conversation-1",
		TurnID:         "turn-1",
		Sequence:       3,
		State:          "responding",
		Payload:        json.RawMessage(`{"text":"你好"}`),
	}

	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"conversationId":"conversation-1","turnId":"turn-1","sequence":3,"state":"responding","payload":{"text":"你好"}}`
	if string(raw) != want {
		t.Fatalf("Event JSON = %s, want %s", raw, want)
	}

	var roundTrip Event
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roundTrip, event) {
		t.Fatalf("Event round trip = %#v, want %#v", roundTrip, event)
	}
}
