package companion

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fairy/model"
)

func TestParticipationEventJSONIsDiagnosticOnly(t *testing.T) {
	inputTokens := uint64(29)
	cachedTokens := uint64(21)
	raw, err := json.Marshal(ParticipationEvent{
		ConversationID: "c1", Generation: 4, EvaluationReason: ParticipationReasonMessage,
		Action: "reply", TargetMessageID: "m4", ObservedAt: time.Unix(1, 0).UTC(),
		Usage: []LaneModelUsage{{
			Lane:  string(model.PromptLaneParticipate),
			Usage: LaneUsage{InputTokens: &inputTokens, CachedInputTokens: CachedTokenObservation{Status: "observed", Tokens: &cachedTokens}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	for _, forbidden := range []string{"sender", "principal", "text", "prompt", "draft", "trace"} {
		if strings.Contains(strings.ToLower(encoded), forbidden) {
			t.Fatalf("diagnostic JSON contains forbidden field %q: %s", forbidden, encoded)
		}
	}
	if !strings.Contains(encoded, `"lane":"participate"`) || !strings.Contains(encoded, `"cachedInputTokens":{"status":"observed","tokens":21}`) {
		t.Fatalf("diagnostic JSON missing planner usage: %s", encoded)
	}
}
