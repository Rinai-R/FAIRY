package companion

import (
	"testing"
)

func TestValidateSubmitTurnRequestRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		request SubmitTurnRequest
	}{
		{name: "missing conversation", request: SubmitTurnRequest{Input: "你好"}},
		{name: "blank conversation", request: SubmitTurnRequest{ConversationID: " ", Input: "你好"}},
		{name: "missing input", request: SubmitTurnRequest{ConversationID: "conversation-1"}},
		{name: "blank input", request: SubmitTurnRequest{ConversationID: "conversation-1", Input: "\t"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateSubmitTurnRequest(tt.request); err == nil {
				t.Fatal("ValidateSubmitTurnRequest() error = nil, want error")
			}
		})
	}
}

func TestValidateSubmitTurnRequestAcceptsValidInput(t *testing.T) {
	if err := ValidateSubmitTurnRequest(SubmitTurnRequest{ConversationID: "conversation-1", Input: "你好"}); err != nil {
		t.Fatalf("ValidateSubmitTurnRequest() error = %v", err)
	}
}

func TestValidateSubmitCompiledTurnRequestRequiresVisualStates(t *testing.T) {
	err := ValidateSubmitCompiledTurnRequest(SubmitCompiledTurnRequest{ConversationID: "conversation-1", Input: "你好", MaxOutputTokens: 160})
	if err == nil {
		t.Fatal("ValidateSubmitCompiledTurnRequest() error = nil, want visual states error")
	}
	if err := ValidateSubmitCompiledTurnRequest(SubmitCompiledTurnRequest{ConversationID: "conversation-1", Input: "你好", MaxOutputTokens: 160, AvailableVisualStates: visualStates("idle", "happy")}); err != nil {
		t.Fatalf("ValidateSubmitCompiledTurnRequest() error = %v", err)
	}
}

func TestValidateReplyChainsAcceptsMissingSpeechText(t *testing.T) {
	if err := ValidateReplyChains([]ReplyChain{{Text: "我在。", VisualState: "idle"}}); err != nil {
		t.Fatalf("ValidateReplyChains() error = %v", err)
	}
}

func TestValidateReplyChainsAcceptsStructuredChains(t *testing.T) {
	err := ValidateReplyChains([]ReplyChain{
		{Text: "你好", VisualState: "happy"},
		{Text: "我在这里。", VisualState: "idle"},
	})
	if err != nil {
		t.Fatalf("ValidateReplyChains() error = %v", err)
	}
}

func TestValidateReplyChainsRejectsInvalidChains(t *testing.T) {
	tests := []struct {
		name   string
		chains []ReplyChain
	}{
		{name: "empty", chains: nil},
		{name: "too many", chains: []ReplyChain{
			{Text: "1", VisualState: "idle"},
			{Text: "2", VisualState: "idle"},
			{Text: "3", VisualState: "idle"},
			{Text: "4", VisualState: "idle"},
			{Text: "5", VisualState: "idle"},
			{Text: "6", VisualState: "idle"},
		}},
		{name: "missing text", chains: []ReplyChain{{VisualState: "idle"}}},
		{name: "missing visual", chains: []ReplyChain{{Text: "x"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateReplyChains(tt.chains); err == nil {
				t.Fatal("ValidateReplyChains() error = nil, want error")
			}
		})
	}
}
