package model

import (
	"strings"
	"testing"
)

func TestValidateContextSegmentsAcceptsOrderedToolPair(t *testing.T) {
	expires := int64(200)
	segments := []ContextSegment{
		{
			ID: "call", Ordinal: 1, Kind: ContextSegmentToolCall,
			Item:            &PromptItem{Type: PromptItemToolCall, ToolCallID: "call-1", ToolName: "memory_search"},
			CreatedAtUnixMS: 100, RetentionPolicy: ContextRetentionCurrentTurn,
			Recoverability: ContextRecoverabilityRequired, ProjectionState: ContextProjectionActive,
		},
		{
			ID: "result", Ordinal: 2, Kind: ContextSegmentToolResult,
			Item:            &PromptItem{Type: PromptItemToolResult, ToolCallID: "call-1", Content: "result"},
			CreatedAtUnixMS: 100, ExpiresAtUnixMS: &expires, RetentionPolicy: ContextRetentionTTL,
			Recoverability: ContextRecoverabilityRefetchable, Dependencies: []string{"call"},
			ProjectionState: ContextProjectionActive,
		},
	}
	if err := ValidateContextSegments(segments); err != nil {
		t.Fatalf("ValidateContextSegments() error = %v", err)
	}
	item, included, err := segments[1].PromptItem()
	if err != nil {
		t.Fatalf("PromptItem() error = %v", err)
	}
	if !included || item.Type != PromptItemToolResult || item.ToolCallID != "call-1" {
		t.Fatalf("PromptItem() = %#v, %t", item, included)
	}
}

func TestContextSegmentValidationRejectsInvalidContracts(t *testing.T) {
	expiresBeforeCreation := int64(9)
	valid := ContextSegment{
		ID: "segment", Ordinal: 1, Kind: ContextSegmentUserMessage,
		Item:            &PromptItem{Type: PromptItemUserMessage, Content: "hello"},
		CreatedAtUnixMS: 10, RetentionPolicy: ContextRetentionRecent,
		Recoverability: ContextRecoverabilityTranscript, ProjectionState: ContextProjectionActive,
	}
	tests := []struct {
		name    string
		mutate  func(*ContextSegment)
		wantErr string
	}{
		{name: "missing id", mutate: func(s *ContextSegment) { s.ID = "" }, wantErr: "id is required"},
		{name: "unknown kind", mutate: func(s *ContextSegment) { s.Kind = "history" }, wantErr: "kind"},
		{name: "type mismatch", mutate: func(s *ContextSegment) {
			s.Item = &PromptItem{Type: PromptItemAssistantMessage, Content: "hello"}
		}, wantErr: "requires prompt item type"},
		{name: "content and ref", mutate: func(s *ContextSegment) { s.ContentRef = "message:1" }, wantErr: "mutually exclusive"},
		{name: "ttl without expiry", mutate: func(s *ContextSegment) {
			s.RetentionPolicy = ContextRetentionTTL
		}, wantErr: "requires expiry"},
		{name: "expiry before creation", mutate: func(s *ContextSegment) {
			s.RetentionPolicy = ContextRetentionTTL
			s.ExpiresAtUnixMS = &expiresBeforeCreation
		}, wantErr: "precedes creation"},
		{name: "self dependency", mutate: func(s *ContextSegment) {
			s.Dependencies = []string{"segment"}
		}, wantErr: "dependency is invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			segment := valid
			test.mutate(&segment)
			if err := segment.Validate(); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestValidateContextSegmentsRejectsOrderAndDependencyErrors(t *testing.T) {
	item := PromptItem{Type: PromptItemContextData, Content: "{}"}
	base := func(id string, ordinal uint64) ContextSegment {
		return ContextSegment{
			ID: id, Ordinal: ordinal, Kind: ContextSegmentContextData, Item: &item,
			RetentionPolicy: ContextRetentionWindow, Recoverability: ContextRecoverabilityRequired,
			ProjectionState: ContextProjectionActive,
		}
	}
	t.Run("duplicate ordinal", func(t *testing.T) {
		err := ValidateContextSegments([]ContextSegment{base("one", 1), base("two", 1)})
		if err == nil || !strings.Contains(err.Error(), "strictly increasing") {
			t.Fatalf("ValidateContextSegments() error = %v", err)
		}
	})
	t.Run("unknown dependency", func(t *testing.T) {
		second := base("two", 2)
		second.Dependencies = []string{"missing"}
		err := ValidateContextSegments([]ContextSegment{base("one", 1), second})
		if err == nil || !strings.Contains(err.Error(), "unknown dependency") {
			t.Fatalf("ValidateContextSegments() error = %v", err)
		}
	})
	t.Run("forward dependency", func(t *testing.T) {
		first := base("one", 1)
		first.Dependencies = []string{"two"}
		err := ValidateContextSegments([]ContextSegment{first, base("two", 2)})
		if err == nil || !strings.Contains(err.Error(), "not earlier") {
			t.Fatalf("ValidateContextSegments() error = %v", err)
		}
	})
}

func TestOmittedContextSegmentDoesNotProjectPromptContent(t *testing.T) {
	segment := ContextSegment{
		ID: "result", Ordinal: 1, Kind: ContextSegmentToolResult,
		Item:            &PromptItem{Type: PromptItemToolResult, ToolCallID: "call-1", Content: "sensitive"},
		RetentionPolicy: ContextRetentionTTL, ExpiresAtUnixMS: int64Pointer(1),
		Recoverability: ContextRecoverabilityRefetchable, ProjectionState: ContextProjectionOmittedL1,
	}
	item, included, err := segment.PromptItem()
	if err != nil {
		t.Fatalf("PromptItem() error = %v", err)
	}
	if included || item != (PromptItem{}) {
		t.Fatalf("PromptItem() = %#v, %t", item, included)
	}
}

func int64Pointer(value int64) *int64 { return &value }
