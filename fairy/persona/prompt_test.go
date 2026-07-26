package persona

import (
	"strings"
	"testing"

	"fairy/character"
	contracts "fairy/contracts/interaction"
	domain "fairy/interaction"
	"fairy/memory"
	"fairy/model"
)

func TestBuildRespondContextSlotsKeepsStableOrder(t *testing.T) {
	slots, err := BuildRespondContextSlots(
		testCharacter(), nil, memory.PromptWindowRecord{Revision: 1},
		[]memory.MessageRecord{{Role: "user", Content: "你好", Sequence: 1}},
		[]VisualState{{ID: "idle", Description: "待机"}}, memory.RetrievalContext{}, privateResolved(),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"character", "display_language", "profile", "available_visual_states", "interaction", "compaction_summary", "dialogue", "retrieved_context"}
	if len(slots) != len(want) {
		t.Fatalf("slots = %d, want %d", len(slots), len(want))
	}
	for index, id := range want {
		if slots[index].ID != id {
			t.Fatalf("slot[%d] = %q, want %q", index, slots[index].ID, id)
		}
	}
}

func TestAppendDesktopInitiationContextUsesContextDataNotUserMessage(t *testing.T) {
	slots, err := AppendDesktopInitiationContext(nil, DesktopInitiationContext{
		Trigger: "lifecycle", Activity: "idle", Lifecycle: "returned",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 1 || slots[0].ID != "desktop_initiation" || len(slots[0].Items) != 1 {
		t.Fatalf("slots = %#v", slots)
	}
	if slots[0].Items[0].Type != model.PromptItemContextData || strings.Contains(slots[0].Items[0].Content, "observationId") {
		t.Fatalf("item = %#v", slots[0].Items[0])
	}
}

func TestBuildRespondContextSlotsAppendsPublicSocialTail(t *testing.T) {
	slots, err := BuildRespondContextSlotsWithSocial(
		testCharacter(), nil, memory.PromptWindowRecord{Revision: 1}, nil,
		[]VisualState{{ID: "idle", Description: "待机"}}, memory.RetrievalContext{}, publicResolved(),
		SocialRespondContext{
			Intent:        &ReplyIntent{ReplyAct: "接话", Tone: "自然", RelationshipSignal: "群友", ReplyMode: "brief", Focus: "当前消息"},
			ContinuityCue: "延续刚才的话题", RecentFeedback: "positive",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := slots[len(slots)-1]; got.ID != "continuity" || got.Trust != "untrusted_context_data" || got.CachePolicy != "tail" {
		t.Fatalf("continuity slot = %#v", got)
	}
	items := PromptItemsFromContextSlots(slots)
	joined := promptText(items)
	if !strings.Contains(joined, `"contextType":"public_reply_intent"`) || !strings.Contains(joined, `"maxChains":1`) {
		t.Fatalf("public social prompt = %s", joined)
	}
}

func TestPrivateRespondContextIncludesCrossGroupSocialMemoryInRetrievedContext(t *testing.T) {
	slots, err := BuildRespondContextSlots(
		testCharacter(), nil, memory.PromptWindowRecord{Revision: 1}, nil,
		[]VisualState{{ID: "idle", Description: "待机"}}, memory.RetrievalContext{
			SocialMemories: memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{{
				ID: "social-1", Kind: memory.SocialMemoryEpisode, Situation: "群里聊找实习", Content: "先问项目背景再给建议",
			}}},
		}, privateResolved(),
	)
	if err != nil {
		t.Fatal(err)
	}
	joined := promptText(PromptItemsFromContextSlots(slots))
	if !strings.Contains(joined, "social-1") || !strings.Contains(joined, "先问项目背景再给建议") {
		t.Fatalf("private retrieved context omitted social memory: %s", joined)
	}
}

func TestPublicRespondContextStripsPrivateAndCrossGroupRetrieval(t *testing.T) {
	slots, err := BuildRespondContextSlots(
		testCharacter(), nil, memory.PromptWindowRecord{Revision: 1}, nil,
		[]VisualState{{ID: "idle", Description: "待机"}}, memory.RetrievalContext{
			PersonalMemories: []memory.RetrievedPersonalMemory{{ID: "private-1", Content: "只属于主人"}},
			SocialMemories: memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{{
				ID: "other-group", Kind: memory.SocialMemoryEpisode, Situation: "其他群", Content: "不应进入公共路径",
			}}},
		}, publicResolved(),
	)
	if err != nil {
		t.Fatal(err)
	}
	joined := promptText(PromptItemsFromContextSlots(slots))
	if strings.Contains(joined, "private-1") || strings.Contains(joined, "只属于主人") || strings.Contains(joined, "other-group") {
		t.Fatalf("public prompt unexpectedly contains private retrieval slots: %s", joined)
	}
}

func TestBuildRespondInputAppliesSummaryCutoff(t *testing.T) {
	summary := "此前用户打过招呼。"
	items, err := BuildRespondInput(
		testCharacter(), nil,
		memory.PromptWindowRecord{Revision: 2, Summary: &summary, CutoffMessageSequence: 2},
		[]memory.MessageRecord{
			{Role: "user", Content: "旧消息", Sequence: 1},
			{Role: "assistant", Content: "旧回复", Sequence: 2},
			{Role: "user", Content: "新消息", Sequence: 3},
		},
		[]VisualState{{ID: "idle", Description: "待机"}}, memory.RetrievalContext{}, privateResolved(),
	)
	if err != nil {
		t.Fatal(err)
	}
	joined := promptText(items)
	if !strings.Contains(joined, summary) || !strings.Contains(joined, "新消息") || strings.Contains(joined, "旧消息") || strings.Contains(joined, "旧回复") {
		t.Fatalf("windowed prompt = %s", joined)
	}
}

func testCharacter() character.Record {
	return character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "认真听用户说话。", TextLanguage: "zh", SpeakingLanguage: "zh"}
}

func privateResolved() domain.Resolved {
	return domain.Resolved{
		Endpoint:  contracts.EndpointDesktop,
		Facts:     contracts.Facts{Audience: contracts.AudienceSingle, Initiation: contracts.InitiationDirect, Presentation: contracts.PresentationEmbodied},
		Principal: domain.PrincipalOwner, Memory: domain.MemoryPersonal,
	}
}

func publicResolved() domain.Resolved {
	return domain.Resolved{
		Endpoint:  contracts.EndpointIM,
		Facts:     contracts.Facts{Audience: contracts.AudienceMulti, Initiation: contracts.InitiationAmbient, Presentation: contracts.PresentationChat},
		Principal: domain.PrincipalNone, Memory: domain.MemoryPublic,
	}
}

func promptText(items []model.PromptItem) string {
	var builder strings.Builder
	for _, item := range items {
		builder.WriteString(item.Content)
		builder.WriteByte('\n')
	}
	return builder.String()
}
