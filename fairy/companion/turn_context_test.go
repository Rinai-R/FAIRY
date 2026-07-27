package companion

import (
	"errors"
	"testing"
)

func TestTurnPhaseErrorPreservesCodeAndCause(t *testing.T) {
	cause := errors.New("gather failed")
	code, got := unwrapTurnPhaseError(&turnPhaseError{code: "GATHER_FAILED", cause: cause})
	if code != "GATHER_FAILED" || !errors.Is(got, cause) {
		t.Fatalf("unwrapTurnPhaseError() = %q, %v", code, got)
	}
}
