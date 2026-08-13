package conversation

import (
	"strings"
	"testing"

	"fairy/agent/reply"
	"fairy/transport/session"
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
		{name: "padded message id", request: SubmitTurnRequest{ConversationID: "conversation-1", Input: "你好", MessageID: " message-1 "}},
		{name: "controlled message id", request: SubmitTurnRequest{ConversationID: "conversation-1", Input: "你好", MessageID: "message\n1"}},
		{name: "invalid utf8 message id", request: SubmitTurnRequest{ConversationID: "conversation-1", Input: "你好", MessageID: string([]byte{0xff})}},
		{name: "long unicode message id", request: SubmitTurnRequest{ConversationID: "conversation-1", Input: "你好", MessageID: strings.Repeat("界", 129)}},
		{name: "padded reply target", request: SubmitTurnRequest{ConversationID: "conversation-1", Input: "你好", ReplyTargetMessageID: " message-1 "}},
		{name: "controlled reply target", request: SubmitTurnRequest{ConversationID: "conversation-1", Input: "你好", ReplyTargetMessageID: "message\n1"}},
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
	if err := ValidateSubmitTurnRequest(SubmitTurnRequest{ConversationID: "conversation-1", Input: "你好", MessageID: strings.Repeat("界", 128), ReplyTargetMessageID: "qq-message-1"}); err != nil {
		t.Fatalf("ValidateSubmitTurnRequest() error = %v", err)
	}
}

func TestAllowsDirectTurnSeparatesConversationModeFromMemoryPolicy(t *testing.T) {
	for _, resolved := range []session.Resolved{
		{Facts: session.Facts{Audience: session.AudienceSingle, Initiation: session.InitiationDirect}, Memory: session.MemoryPersonal},
		{Facts: session.Facts{Audience: session.AudienceSingle, Initiation: session.InitiationDirect}, Memory: session.MemoryPublic},
	} {
		if !allowsDirectTurn(resolved) {
			t.Fatalf("single direct interaction rejected: %#v", resolved)
		}
	}
	for _, resolved := range []session.Resolved{
		{Facts: session.Facts{Audience: session.AudienceMulti, Initiation: session.InitiationAmbient}, Memory: session.MemoryPublic},
		{Facts: session.Facts{Audience: session.AudienceSingle, Initiation: session.InitiationAmbient}, Memory: session.MemoryPublic},
	} {
		if allowsDirectTurn(resolved) {
			t.Fatalf("non-direct interaction accepted: %#v", resolved)
		}
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

func TestValidateSubmitCompiledTurnRequestRejectsInvalidMessageID(t *testing.T) {
	err := ValidateSubmitCompiledTurnRequest(SubmitCompiledTurnRequest{
		ConversationID: "conversation-1", Input: "你好", MessageID: " message-1 ",
		MaxOutputTokens: 160, AvailableVisualStates: visualStates("idle"),
	})
	if err == nil {
		t.Fatal("ValidateSubmitCompiledTurnRequest() error = nil, want message id error")
	}
}

func TestValidateSubmitCompiledTurnRequestRejectsInvalidReplyTargetMessageID(t *testing.T) {
	err := ValidateSubmitCompiledTurnRequest(SubmitCompiledTurnRequest{
		ConversationID: "conversation-1", Input: "你好", ReplyTargetMessageID: " target ",
		MaxOutputTokens: 160, AvailableVisualStates: visualStates("idle"),
	})
	if err == nil || !strings.Contains(err.Error(), "reply_target_message_id") {
		t.Fatalf("ValidateSubmitCompiledTurnRequest() error = %v, want reply target error", err)
	}
}

func TestValidateSubmitCompiledTurnRequestAcceptsInitiationWithoutInput(t *testing.T) {
	err := ValidateSubmitCompiledTurnRequest(SubmitCompiledTurnRequest{
		ConversationID: "conversation-1", MaxOutputTokens: 160, AvailableVisualStates: visualStates("idle"),
		Initiation: &DesktopInitiationContext{ObservationEvidenceIDs: []string{"obs-1"}, Trigger: "lifecycle", Lifecycle: "returned"},
	})
	if err != nil {
		t.Fatal(err)
	}
	both := SubmitCompiledTurnRequest{
		ConversationID: "conversation-1", Input: "fabricated", MaxOutputTokens: 160, AvailableVisualStates: visualStates("idle"),
		Initiation: &DesktopInitiationContext{ObservationEvidenceIDs: []string{"obs-1"}, Trigger: "lifecycle"},
	}
	if err := ValidateSubmitCompiledTurnRequest(both); err == nil {
		t.Fatal("validator accepted both user input and initiation context")
	}
}

func TestValidateDesktopInitiationRequestRequiresEvidenceWithoutInput(t *testing.T) {
	if err := ValidateDesktopInitiationRequest(DesktopInitiationRequest{ConversationID: "conversation-1", ObservationEvidenceIDs: []string{"obs-1"}}); err != nil {
		t.Fatalf("valid initiation request: %v", err)
	}
	for _, request := range []DesktopInitiationRequest{
		{},
		{ConversationID: "conversation-1"},
		{ConversationID: "conversation-1", ObservationEvidenceIDs: []string{" "}},
		{ConversationID: "conversation-1", ObservationEvidenceIDs: []string{" observation "}},
		{ConversationID: "conversation-1", ObservationEvidenceIDs: []string{"observation\n1"}},
		{ConversationID: "conversation-1", ObservationEvidenceIDs: []string{strings.Repeat("界", 129)}},
	} {
		if err := ValidateDesktopInitiationRequest(request); err == nil {
			t.Fatalf("ValidateDesktopInitiationRequest(%#v) error = nil", request)
		}
	}
}

func TestValidateReplyChainsAcceptsUtterance(t *testing.T) {
	if err := reply.ValidateReplyChains([]reply.ReplyChain{{Text: "我在。", VisualState: "idle"}}); err != nil {
		t.Fatalf("reply.ValidateReplyChains() error = %v", err)
	}
}

func TestValidateReplyChainsAcceptsStructuredChains(t *testing.T) {
	err := reply.ValidateReplyChains([]reply.ReplyChain{
		{Text: "你好", VisualState: "happy"},
		{Text: "我在这里。", VisualState: "idle"},
	})
	if err != nil {
		t.Fatalf("reply.ValidateReplyChains() error = %v", err)
	}
}

func TestValidateReplyChainsRejectsInvalidChains(t *testing.T) {
	tests := []struct {
		name   string
		chains []reply.ReplyChain
	}{
		{name: "empty", chains: nil},
		{name: "too many", chains: []reply.ReplyChain{
			{Text: "1", VisualState: "idle"},
			{Text: "2", VisualState: "idle"},
			{Text: "3", VisualState: "idle"},
			{Text: "4", VisualState: "idle"},
			{Text: "5", VisualState: "idle"},
			{Text: "6", VisualState: "idle"},
			{Text: "7", VisualState: "idle"},
			{Text: "8", VisualState: "idle"},
			{Text: "9", VisualState: "idle"},
			{Text: "10", VisualState: "idle"},
			{Text: "11", VisualState: "idle"},
			{Text: "12", VisualState: "idle"},
			{Text: "13", VisualState: "idle"},
		}},
		{name: "missing text", chains: []reply.ReplyChain{{VisualState: "idle"}}},
		{name: "missing visual", chains: []reply.ReplyChain{{Text: "x"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := reply.ValidateReplyChains(tt.chains); err == nil {
				t.Fatal("reply.ValidateReplyChains() error = nil, want error")
			}
		})
	}
}
