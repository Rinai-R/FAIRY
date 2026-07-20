//go:build integration

package memory

import (
	"fmt"
	"testing"
)

func appendModelUsageEvent(t *testing.T, store *Store, conversationID, turnID, lane string, input, output uint64, cached *uint64) {
	t.Helper()
	cachedJSON := `{"status":"missing","tokens":null}`
	if cached != nil {
		cachedJSON = fmt.Sprintf(`{"status":"observed","tokens":%d}`, *cached)
	}
	metadata := fmt.Sprintf(
		`{"streamEventCount":1,"usage":[{"lane":%q,"historyWindow":1,"usage":{"inputTokens":%d,"outputTokens":%d,"cachedInputTokens":%s,"cacheWriteTokens":{"status":"missing","tokens":null}}}]}`,
		lane, input, output, cachedJSON,
	)
	if _, err := store.AppendTurnRuntimeEvent(TurnRuntimeEventInput{ConversationID: conversationID, TurnID: turnID, EventType: "model", MetadataJSON: metadata}); err != nil {
		t.Fatalf("AppendTurnRuntimeEvent(model): %v", err)
	}
}

func appendTerminalEvent(t *testing.T, store *Store, conversationID, turnID, status string) {
	t.Helper()
	if _, err := store.AppendTurnRuntimeEvent(TurnRuntimeEventInput{ConversationID: conversationID, TurnID: turnID, EventType: "terminal", State: &status, MetadataJSON: fmt.Sprintf(`{"status":%q}`, status)}); err != nil {
		t.Fatalf("AppendTurnRuntimeEvent(terminal): %v", err)
	}
}

func findLane(t *testing.T, lanes []UsageLaneAggregate, lane string) UsageLaneAggregate {
	t.Helper()
	for _, aggregate := range lanes {
		if aggregate.Lane == lane {
			return aggregate
		}
	}
	t.Fatalf("lane %q not found in %#v", lane, lanes)
	return UsageLaneAggregate{}
}
