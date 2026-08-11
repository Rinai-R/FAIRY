package extraction

import (
	"strings"
	"testing"

	"fairy/context/memory/personal"
)

func TestMutationContentLimitMatchesDirectWrite(t *testing.T) {
	mutation := Mutation{
		Operation: OperationAdd, SourceTurnID: "turn-1", Kind: "experience",
		Scope: personal.Scope{Type: "global"}, Content: strings.Repeat("事", personal.MaxContentRunes+1),
		ConfidenceBasisPoints: 9000,
	}
	if err := ValidateMutation(&mutation, "character-1"); err == nil || !strings.Contains(err.Error(), "2400") {
		t.Fatalf("ValidateMutation(over limit) error = %v", err)
	}
}
