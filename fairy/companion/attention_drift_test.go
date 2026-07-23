package companion

import (
	"strings"
	"testing"
)

func TestEncodeReplyIntentContextIncludesDriftGuidance(t *testing.T) {
	item, err := encodeReplyIntentContext(ReplyIntent{
		ReplyAct: "接话", Tone: "自然", RelationshipSignal: "群友", ReplyMode: "brief",
		Focus: "当前消息", ExpressionQuery: "轻松接话", DriftLevel: DriftScattered, AnchorPolicy: AnchorLoose,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(item.Content, `"driftLevel":"scattered"`) || !strings.Contains(item.Content, `"anchorPolicy":"loose"`) {
		t.Fatalf("payload = %s", item.Content)
	}
	if !strings.Contains(item.Content, "明显发散") || !strings.Contains(item.Content, "宽松关联") {
		t.Fatalf("missing guidance: %s", item.Content)
	}
}

func TestCompileReplyIntentAppliesDriftDefaults(t *testing.T) {
	raw := []byte(`{"replyAct":"接话","tone":"自然","relationshipSignal":"群友","replyMode":"brief","focus":"当前消息","avoid":[],"referenceInfo":"","memoryQuery":"","expressionQuery":"轻松接话"}`)
	intent, err := compileReplyIntent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if intent.DriftLevel != defaultDriftLevel || intent.AnchorPolicy != defaultAnchorPolicy {
		t.Fatalf("defaults = %#v", intent)
	}
}

func TestCompileReplyIntentRejectsInvalidDrift(t *testing.T) {
	raw := []byte(`{"replyAct":"接话","tone":"自然","relationshipSignal":"群友","replyMode":"brief","focus":"当前消息","avoid":[],"referenceInfo":"","memoryQuery":"","expressionQuery":"轻松接话","driftLevel":"chaotic"}`)
	if _, err := compileReplyIntent(raw); err == nil {
		t.Fatal("invalid drift accepted")
	}
}
