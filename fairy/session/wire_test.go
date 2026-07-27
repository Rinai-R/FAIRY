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
	}

	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"endpoint":"im","endpointKey":"onebot-group:123","interaction":{"audience":"multi","initiation":"ambient","presentation":"chat"}}`
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
