package conversation

import (
	"encoding/json"
	"testing"
)

func decodeEventPayload[T any](t testing.TB, raw json.RawMessage) T {
	t.Helper()
	var payload T
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode event payload: %v", err)
	}
	return payload
}
