package session

import (
	"encoding/json"
	"reflect"
	"strings"
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

func TestOpenRequestCharacterAndEvaluationJSONContract(t *testing.T) {
	request := OpenRequest{
		Endpoint: EndpointDesktop, EndpointKey: "web-evaluation-1", CharacterID: "character-2",
		Interaction: Context{Audience: AudienceSingle, Initiation: InitiationDirect, Presentation: PresentationChat, Evaluation: true},
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"endpoint":"desktop","endpointKey":"web-evaluation-1","characterId":"character-2","interaction":{"audience":"single","initiation":"direct","presentation":"chat","evaluation":true},"outputCapabilities":{"sticker":false}}`
	if string(raw) != want {
		t.Fatalf("OpenRequest JSON = %s, want %s", raw, want)
	}
}

func TestExpressionDeliveryResultValidation(t *testing.T) {
	base := ExpressionDeliveryResult{
		ConversationID: "conversation-1", TurnID: "turn-1", BeatID: "final-0",
		Status: ExpressionDeliverySucceeded,
	}
	tests := []struct {
		name    string
		mutate  func(*ExpressionDeliveryResult)
		wantErr bool
	}{
		{name: "local success"},
		{name: "external success", mutate: func(result *ExpressionDeliveryResult) { result.ExternalMessageID = "45123" }},
		{name: "unicode boundary", mutate: func(result *ExpressionDeliveryResult) { result.ExternalMessageID = strings.Repeat("界", 128) }},
		{name: "failed", mutate: func(result *ExpressionDeliveryResult) {
			result.Status = ExpressionDeliveryFailed
			result.ErrorMessage = "surface send failed"
		}},
		{name: "failed without error", mutate: func(result *ExpressionDeliveryResult) {
			result.Status = ExpressionDeliveryFailed
		}, wantErr: true},
		{name: "failed with external ID", mutate: func(result *ExpressionDeliveryResult) {
			result.Status = ExpressionDeliveryFailed
			result.ErrorMessage = "surface send failed"
			result.ExternalMessageID = "45123"
		}, wantErr: true},
		{name: "leading whitespace", mutate: func(result *ExpressionDeliveryResult) { result.ExternalMessageID = " 45123" }, wantErr: true},
		{name: "trailing whitespace", mutate: func(result *ExpressionDeliveryResult) { result.ExternalMessageID = "45123 " }, wantErr: true},
		{name: "control", mutate: func(result *ExpressionDeliveryResult) { result.ExternalMessageID = "45\n123" }, wantErr: true},
		{name: "invalid utf8", mutate: func(result *ExpressionDeliveryResult) { result.ExternalMessageID = string([]byte{0xff}) }, wantErr: true},
		{name: "too long", mutate: func(result *ExpressionDeliveryResult) { result.ExternalMessageID = strings.Repeat("界", 129) }, wantErr: true},
		{name: "success with error", mutate: func(result *ExpressionDeliveryResult) { result.ErrorMessage = "unexpected" }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := base
			if tt.mutate != nil {
				tt.mutate(&result)
			}
			if err := result.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestExpressionDeliveryResultJSONContractIncludesExternalMessageID(t *testing.T) {
	result := ExpressionDeliveryResult{
		ConversationID: "conversation-1", TurnID: "turn-1", BeatID: "final-0",
		Status: ExpressionDeliverySucceeded, ExternalMessageID: "45123",
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"conversationId":"conversation-1","turnId":"turn-1","beatId":"final-0","status":"succeeded","externalMessageId":"45123"}`
	if string(raw) != want {
		t.Fatalf("ExpressionDeliveryResult JSON = %s, want %s", raw, want)
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
