package character

import (
	historyexpr "fairy/context/history/expression"
	history "fairy/context/history/transcript"
	"fairy/context/memory/personal"
	"fairy/context/recall"
	"fairy/context/social"
	"fairy/runtime/model"
	"fairy/transport/session"
	"strings"
	"testing"
)

func TestBuildRespondContextSlotsKeepsStableOrder(t *testing.T) {
	slots, err := BuildRespondContextSlots(
		testCharacter(), nil, history.PromptWindowRecord{Revision: 1},
		[]history.MessageRecord{{Role: "user", Content: "你好", Sequence: 1}},
		[]VisualState{{ID: "idle", Description: "待机"}}, recall.Context{}, privateResolved(),
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
		testCharacter(), nil, history.PromptWindowRecord{Revision: 1}, nil,
		[]VisualState{{ID: "idle", Description: "待机"}}, recall.Context{}, publicResolved(),
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
		testCharacter(), nil, history.PromptWindowRecord{Revision: 1}, nil,
		[]VisualState{{ID: "idle", Description: "待机"}}, recall.Context{
			SocialMemories: social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{{
				ID: "social-1", Kind: social.SocialMemoryEpisode, Situation: "群里聊找实习", Content: "先问项目背景再给建议",
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
		testCharacter(), nil, history.PromptWindowRecord{Revision: 1}, nil,
		[]VisualState{{ID: "idle", Description: "待机"}}, recall.Context{
			PersonalMemories: []personal.Retrieved{{ID: "private-1", Content: "只属于主人"}},
			SocialMemories: social.SocialMemoryContext{Entries: []social.SocialMemoryEntry{{
				ID: "other-group", Kind: social.SocialMemoryEpisode, Situation: "其他群", Content: "不应进入公共路径",
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
		history.PromptWindowRecord{Revision: 2, Summary: &summary, CutoffMessageSequence: 2},
		[]history.MessageRecord{
			{Role: "user", Content: "旧消息", Sequence: 1},
			{Role: "assistant", Content: "旧回复", Sequence: 2},
			{Role: "user", Content: "新消息", Sequence: 3},
		},
		[]VisualState{{ID: "idle", Description: "待机"}}, recall.Context{}, privateResolved(),
	)
	if err != nil {
		t.Fatal(err)
	}
	joined := promptText(items)
	if !strings.Contains(joined, summary) || !strings.Contains(joined, "新消息") || strings.Contains(joined, "旧消息") || strings.Contains(joined, "旧回复") {
		t.Fatalf("windowed prompt = %s", joined)
	}
}

func TestBuildRespondInputProjectsStickerHistoryAsSafeText(t *testing.T) {
	items, err := BuildRespondInput(
		testCharacter(), nil, history.PromptWindowRecord{Revision: 1},
		[]history.MessageRecord{{
			Role: "assistant", Content: "我懂了。", Sequence: 1,
			Parts: []historyexpr.Part{
				{Kind: historyexpr.Utterance, Text: "我懂了。", VisualState: "idle"},
				{Kind: historyexpr.Sticker, VisualState: "happy", Sticker: &historyexpr.StickerSnapshot{
					ID: "sticker-secret-id", Description: "开心赞同", MIMEType: "image/webp",
				}},
			},
		}},
		[]VisualState{{ID: "idle", Description: "待机"}}, recall.Context{}, privateResolved(),
	)
	if err != nil {
		t.Fatal(err)
	}
	joined := promptText(items)
	if !strings.Contains(joined, "我懂了。\n[表情包：开心赞同]") {
		t.Fatalf("prompt omitted ordered sticker history: %s", joined)
	}
	for _, forbidden := range []string{"sticker-secret-id", "image/webp"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("prompt leaked %q: %s", forbidden, joined)
		}
	}
}

func testCharacter() Record {
	return Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "认真听用户说话。", TextLanguage: "zh", SpeakingLanguage: "zh"}
}

func privateResolved() session.Resolved {
	return session.Resolved{
		Endpoint:  session.EndpointDesktop,
		Facts:     session.Facts{Audience: session.AudienceSingle, Initiation: session.InitiationDirect, Presentation: session.PresentationEmbodied},
		Principal: session.PrincipalOwner, Memory: session.MemoryPersonal,
	}
}

func publicResolved() session.Resolved {
	return session.Resolved{
		Endpoint:  session.EndpointIM,
		Facts:     session.Facts{Audience: session.AudienceMulti, Initiation: session.InitiationAmbient, Presentation: session.PresentationChat},
		Principal: session.PrincipalNone, Memory: session.MemoryPublic,
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
