package personal

import (
	"strings"
	"testing"
)

func TestContentLimitUsesUnicodeRunes(t *testing.T) {
	if MaxContentRunes != 2400 {
		t.Fatalf("MaxContentRunes = %d, want 2400", MaxContentRunes)
	}
	valid := strings.Repeat("忆", MaxContentRunes)
	if err := ValidateInput("preference", Scope{Type: "global"}, valid, 9000); err != nil {
		t.Fatalf("ValidateInput(exact limit) error = %v", err)
	}
	tooLong := valid + "忆"
	if err := ValidateInput("preference", Scope{Type: "global"}, tooLong, 9000); err == nil || !strings.Contains(err.Error(), "2400") {
		t.Fatalf("ValidateInput(over limit) error = %v", err)
	}
}
