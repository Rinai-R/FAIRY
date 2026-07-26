package memory

import (
	"strings"
	"testing"
)

func TestPersonalMemoryContentLimitUsesUnicodeRunes(t *testing.T) {
	if MaxPersonalMemoryContentRunes != 2400 {
		t.Fatalf("MaxPersonalMemoryContentRunes = %d, want 2400", MaxPersonalMemoryContentRunes)
	}
	valid := strings.Repeat("忆", MaxPersonalMemoryContentRunes)
	if err := ValidateMemoryInput("preference", MemoryScope{Type: "global"}, valid, 9000); err != nil {
		t.Fatalf("validateMemoryInput(exact limit) error = %v", err)
	}
	tooLong := valid + "忆"
	if err := ValidateMemoryInput("preference", MemoryScope{Type: "global"}, tooLong, 9000); err == nil || !strings.Contains(err.Error(), "2400") {
		t.Fatalf("validateMemoryInput(over limit) error = %v", err)
	}
}

func TestMemoryMutationContentLimitMatchesDirectWrite(t *testing.T) {
	mutation := MemoryMutation{
		Operation:             "create",
		SourceTurnID:          "turn-1",
		Kind:                  "experience",
		Scope:                 MemoryScope{Type: "global"},
		Content:               strings.Repeat("事", MaxPersonalMemoryContentRunes+1),
		ConfidenceBasisPoints: 9000,
	}
	if err := ValidateMemoryMutation(&mutation, "character-1"); err == nil || !strings.Contains(err.Error(), "2400") {
		t.Fatalf("validateMemoryMutation(over limit) error = %v", err)
	}
}
