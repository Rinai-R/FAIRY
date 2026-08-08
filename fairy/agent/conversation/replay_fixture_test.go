package conversation

import (
	"encoding/json"
	"strings"
	"testing"

	"fairy/agent/reply"
	"fairy/context/character"
	history "fairy/context/history/transcript"
	"fairy/context/knowledge"
	"fairy/context/recall"
	"fairy/runtime/model"
)

// TestReplayFixture keeps the production prompt/reply boundary deterministic
// without invoking a provider or exposing internal decision fields.
func TestReplayFixture(t *testing.T) {
	record := character.Record{CharacterID: "replay-character", Revision: 3, Name: "亚托莉", Description: "认真听用户说话。", TextLanguage: "zh", SpeakingLanguage: "zh"}
	resolved := publicAmbientResolved()
	intent := &ReplyIntent{ReplyAct: "接话", Tone: "自然", RelationshipSignal: "群友", ReplyMode: "brief", Focus: "当前消息"}
	retrieval := recall.Context{Knowledge: []knowledge.Retrieved{{ID: "knowledge-1", Statement: "公开事实"}}}
	slots, err := BuildRespondContextSlotsWithSocial(record, nil, history.PromptWindowRecord{Revision: 2}, []history.MessageRecord{{Role: "user", Content: "当前消息", Sequence: 4}}, []reply.VisualState{{ID: "idle", Description: "待机"}}, retrieval, resolved, SocialRespondContext{Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	items := character.PromptItemsFromContextSlots(slots)
	if got := contextSlotIDs(slots); !strings.Contains(got, "character,display_language") || len(slots) < 9 || slots[8].ID != "reply_intent" {
		t.Fatalf("replay slot order = %s", got)
	}
	cacheInput := model.NewCacheKeyInput(model.PromptLaneRespond, "replay-model", "replay-conversation", RespondInstructions)
	metadata := runtimePromptLedgerMetadata(items, slots, history.PromptWindowRecord{Revision: 2}, nil, nil, retrieval, cacheInput, true)
	wire, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"stance", "replyIntent", "relationshipSignal", "decision", "当前消息", "公开事实"} {
		if strings.Contains(string(wire), forbidden) {
			t.Fatalf("replay ledger leaked %q: %s", forbidden, wire)
		}
	}

	draft := testRespondEnvelope(testReplyChain{VisualState: "idle", Text: "收到，我先陪你缓一缓。"})
	reply, err := compileReplyForInteraction(draft, []reply.VisualState{{ID: "idle", Description: "待机"}}, resolved, intent)
	if err != nil || len(reply.Chains) != 1 {
		t.Fatalf("replay reply = %#v, error=%v", reply, err)
	}
	if err := validateReplyForInteraction(reply, resolved, intent); err != nil {
		t.Fatal(err)
	}
}

func contextSlotIDs(slots []ContextSlot) string {
	ids := make([]string, 0, len(slots))
	for _, slot := range slots {
		ids = append(ids, slot.ID)
	}
	return strings.Join(ids, ",")
}
