package companion

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"fairy/character"
	"fairy/memory"
	"fairy/model"
	"fairy/profile"
)

func TestRespondInstructionsStayStable(t *testing.T) {
	// Exact strings define the Go/Wails production prompt contract.
	const stableRespond = RespondInstructions
	const stableCompact = "FAIRY conversation compactor v2. Return only a concise plain-text summary of meaningful user and assistant dialogue for future companion turns. Exclude developer instructions, obsolete character revisions, obsolete user names, cache metadata, and duplicate canonical context. Do not invent facts or wrap the summary in JSON or Markdown."
	const stableExtract = "Read the supplied conversation batch and existing personal memories. Return exactly one JSON object: {\"mutations\": [...]}. A mutation operation is either create with kind, scope, content, confidenceBasisPoints; or supersede with memoryId plus the same fields. Use only memory IDs supplied in existingMemories. Map durable companion observations into existing kinds: profile for stable user traits and communication style; preference for likes, dislikes, support expectations, and interaction preferences; experience for recurring life context or meaningful events explicitly described by the user; relationship for current-character-specific trust, closeness, boundaries, and pacing cues. preference, profile, and experience use global scope; relationship uses the supplied current character scope. Record only durable facts directly supported by the dialogue. Do not record transient emotions, diagnoses, unsupported personality judgments, hidden analysis traces, or unsupported role strategies as facts. Return an empty mutations array when nothing should change. Do not output Markdown, reasoning, delete, or tombstone operations."
	if RespondInstructions != stableRespond {
		t.Fatalf("RespondInstructions changed unexpectedly (%d vs %d runes)", utf8.RuneCountInString(RespondInstructions), utf8.RuneCountInString(stableRespond))
	}
	if CompactInstructions != stableCompact {
		t.Fatal("CompactInstructions changed unexpectedly")
	}
	if ExtractInstructions != stableExtract {
		t.Fatal("ExtractInstructions changed unexpectedly")
	}
	if strings.Contains(RespondInstructions, "VISUAL_STATE:") || strings.Contains(RespondInstructions, "web_search") {
		t.Fatal("forbidden protocol fragments present")
	}
	for _, required := range []string{
		"strict JSON object", `"chains"`, "the character's spoken line", "chains length is 1-5",
		"stance", "replyIntent", "tone", "relationshipSignal", "replyMode", "brief|normal|expanded",
		"Never output decision", "untrusted data", "write the next natural line",
		"without mechanically repeating profanity or memes", "Keep everyday chat concise",
		"acknowledge it first in a short line", "do not rush into solutions",
		"Do not pretend to perform real-world or code actions", "Preferred name is optional",
	} {
		if !strings.Contains(RespondInstructions, required) {
			t.Fatalf("RespondInstructions missing %q", required)
		}
	}
	for _, forbidden := range []string{"嗯", "稍等", "wait-filler", "thinking beat", "surprised beat"} {
		if strings.Contains(RespondInstructions, forbidden) {
			t.Fatalf("RespondInstructions must not prime filler dialogue with %q", forbidden)
		}
	}
	if strings.Contains(RespondInstructions, `"decision":`) {
		t.Fatal("RespondInstructions must not request a decision JSON field")
	}
	if utf8.RuneCountInString(RespondInstructions) >= 2200 {
		t.Fatalf("restored respond instructions are too long: %d runes", utf8.RuneCountInString(RespondInstructions))
	}
}

func TestPublicRespondInstructionsRequireImmediateSingleHook(t *testing.T) {
	instructions := RespondInstructionsForInteraction(false, publicAmbientResolved())
	for _, required := range []string{"one conversational hook", "not a summary of the whole transcript", "do not turn a reaction into unsolicited advice"} {
		if !strings.Contains(instructions, required) {
			t.Fatalf("public Respond instructions missing %q", required)
		}
	}
	for _, forbidden := range []string{"one conversational hook", "unsolicited advice", "concluding lecture"} {
		if strings.Contains(RespondInstructions, forbidden) {
			t.Fatalf("base Respond instructions unexpectedly contain public-only rule %q", forbidden)
		}
	}
}

func TestExtractInstructionsDescribeCompanionMemoryKinds(t *testing.T) {
	for _, required := range []string{
		"communication style",
		"support expectations",
		"interaction preferences",
		"recurring life context",
		"current-character-specific trust",
		"boundaries",
		"pacing cues",
		"Do not record transient emotions",
		"diagnoses",
		"hidden analysis traces",
		"unsupported role strategies",
		"Do not output Markdown, reasoning, delete, or tombstone operations",
	} {
		if !strings.Contains(ExtractInstructions, required) {
			t.Fatalf("ExtractInstructions missing %q", required)
		}
	}
}

func TestBuildRespondContextSlotsKeepsStableOrderAndOmissionMetadata(t *testing.T) {
	slots, err := BuildRespondContextSlots(
		character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "认真听用户说话。", TextLanguage: character.DefaultTextLanguage, SpeakingLanguage: character.DefaultSpeakingLanguage},
		nil,
		memory.PromptWindowRecord{Revision: 1},
		[]memory.MessageRecord{{Role: "user", Content: "你好", Sequence: 1}},
		[]VisualState{{ID: "idle", Description: "待机"}},
		memory.RetrievalContext{},
		desktopResolved(),
	)
	if err != nil {
		t.Fatalf("BuildRespondContextSlots() error = %v", err)
	}
	wantIDs := []string{"character", "display_language", "profile", "available_visual_states", "interaction", "compaction_summary", "dialogue", "retrieved_context"}
	if len(slots) != len(wantIDs) {
		t.Fatalf("slots len = %d, want %d: %#v", len(slots), len(wantIDs), slots)
	}
	for i, want := range wantIDs {
		if slots[i].ID != want {
			t.Fatalf("slot[%d].ID = %q, want %q; slots=%#v", i, slots[i].ID, want, slots)
		}
		if slots[i].RevisionHash == "" && slots[i].Present {
			t.Fatalf("present slot %q missing revision hash: %#v", slots[i].ID, slots[i])
		}
	}
	if slots[5].Present || slots[5].OmitReason != "empty" {
		t.Fatalf("compaction_summary slot = %#v, want omitted empty", slots[5])
	}
	if slots[7].Present || slots[7].OmitReason != "empty" {
		t.Fatalf("retrieved_context slot = %#v, want omitted empty", slots[7])
	}
	items := PromptItemsFromContextSlots(slots)
	if len(items) != 6 {
		t.Fatalf("items len = %d, want 6: %#v", len(items), items)
	}
	if !strings.Contains(items[4].Content, `"endpoint":"desktop"`) {
		t.Fatalf("interaction item = %q, want desktop endpoint", items[4].Content)
	}
}

func TestBuildRespondContextSlotsAppendsPublicSocialContextAfterStablePrefix(t *testing.T) {
	record := character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"}
	messages := []memory.MessageRecord{{Role: "user", Content: "最近找实习有点慌", Sequence: 1}}
	states := []VisualState{{ID: "idle", Description: "待机"}}
	intent := &ReplyIntent{
		ReplyAct: "先接住情绪", Tone: "自然克制", RelationshipSignal: "熟悉的群友", ReplyMode: "brief",
		Focus: "对找实习的焦虑", Avoid: []string{"说教"}, ReferenceInfo: "对方刚开始投递", ExpressionQuery: "安慰焦虑的群友",
	}
	first, err := BuildRespondContextSlotsWithSocial(record, nil, memory.PromptWindowRecord{Revision: 1}, messages, states, memory.RetrievalContext{}, publicAmbientResolved(), SocialRespondContext{
		Intent: intent,
		Memory: memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{{
			ID: "entry-private-internal-1", CharacterID: "character-1", ConversationID: "conversation-1",
			Kind: memory.SocialMemoryExpression, Situation: "群友为求职焦虑", Content: "先轻轻接住情绪，再给一个具体小建议",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildRespondContextSlotsWithSocial(record, nil, memory.PromptWindowRecord{Revision: 1}, messages, states, memory.RetrievalContext{}, publicAmbientResolved(), SocialRespondContext{
		Intent: intent,
		Memory: memory.SocialMemoryContext{Entries: []memory.SocialMemoryEntry{{
			ID: "entry-private-internal-2", CharacterID: "character-1", ConversationID: "conversation-1",
			Kind: memory.SocialMemoryBehavior, Situation: "群友为求职焦虑", Content: "先询问当前卡点，让建议落到眼前一步",
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"character", "display_language", "profile", "available_visual_states", "interaction", "compaction_summary", "dialogue", "retrieved_context", "reply_intent", "social_memory", "person_notes"}
	if len(first) != len(wantIDs) || len(second) != len(wantIDs) {
		t.Fatalf("slot lengths = %d, %d", len(first), len(second))
	}
	stableUntil := 9 // reply_intent is last shared control slot before dynamic social payloads
	for index, id := range wantIDs {
		if first[index].ID != id || second[index].ID != id {
			t.Fatalf("slot[%d] = (%q, %q), want %q", index, first[index].ID, second[index].ID, id)
		}
		if index < stableUntil && first[index].RevisionHash != second[index].RevisionHash {
			t.Fatalf("stable slot %q changed across dynamic candidates", id)
		}
	}
	firstItems := PromptItemsFromContextSlots(first)
	secondItems := PromptItemsFromContextSlots(second)
	if len(firstItems) != len(secondItems) {
		t.Fatalf("item lengths = %d, %d", len(firstItems), len(secondItems))
	}
	for index := 0; index < len(firstItems)-1; index++ {
		if firstItems[index] != secondItems[index] {
			t.Fatalf("prompt prefix item %d changed", index)
		}
	}
	intentJSON := firstItems[len(firstItems)-2].Content
	if !strings.Contains(intentJSON, `"contextType":"public_reply_intent"`) || !strings.Contains(intentJSON, `"replyMode":"brief"`) ||
		!strings.Contains(intentJSON, `"delivery":{"minChains":1,"maxChains":1,"oneConversationalHook":true,"avoidUnrequestedAdvice":true}`) {
		t.Fatalf("reply intent context = %s", intentJSON)
	}
	memoryJSON := firstItems[len(firstItems)-1].Content
	for _, forbidden := range []string{"entry-private-internal-1", "character-1", "conversation-1", "positive_count", "private"} {
		if strings.Contains(memoryJSON, forbidden) {
			t.Fatalf("social memory prompt leaked %q: %s", forbidden, memoryJSON)
		}
	}
	if !strings.Contains(memoryJSON, `"contextType":"public_social_memory"`) || !strings.Contains(memoryJSON, "先轻轻接住情绪") {
		t.Fatalf("social memory context = %s", memoryJSON)
	}
}

func TestReplyIntentDeliveryContractFollowsModeWithoutChangingInstructions(t *testing.T) {
	tests := []struct {
		mode string
		max  int
	}{
		{mode: "brief", max: 1},
		{mode: "normal", max: 3},
		{mode: "expanded", max: 5},
	}
	publicInstructions := RespondInstructionsForInteraction(false, publicAmbientResolved())
	for _, tt := range tests {
		item, err := encodeReplyIntentContext(ReplyIntent{
			ReplyAct: "接话", Tone: "自然", RelationshipSignal: "群友", ReplyMode: tt.mode, Focus: "一个话题",
		})
		if err != nil {
			t.Fatalf("mode %q: %v", tt.mode, err)
		}
		var payload replyIntentContextPayload
		if err := json.Unmarshal([]byte(item.Content), &payload); err != nil {
			t.Fatalf("mode %q decode: %v", tt.mode, err)
		}
		if payload.Delivery.MinChains != 1 || payload.Delivery.MaxChains != tt.max || !payload.Delivery.OneConversationalHook || !payload.Delivery.AvoidUnrequestedAdvice {
			t.Fatalf("mode %q delivery = %#v", tt.mode, payload.Delivery)
		}
		if got := RespondInstructionsForInteraction(false, publicAmbientResolved()); got != publicInstructions {
			t.Fatalf("public instructions changed for mode %q", tt.mode)
		}
	}
}

func TestBuildRespondContextSlotsRejectsSocialContextForPrivateInteraction(t *testing.T) {
	_, err := BuildRespondContextSlotsWithSocial(
		character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "陪伴", TextLanguage: "zh", SpeakingLanguage: "zh"},
		nil, memory.PromptWindowRecord{Revision: 1}, nil, []VisualState{{ID: "idle", Description: "待机"}}, memory.RetrievalContext{}, desktopResolved(),
		SocialRespondContext{Intent: &ReplyIntent{ExpressionQuery: "聊天"}},
	)
	if err == nil {
		t.Fatal("BuildRespondContextSlotsWithSocial() error = nil")
	}
}

func TestBuildRespondContextSlotsAppendsRecentSameParticipantReply(t *testing.T) {
	slots, err := BuildRespondContextSlotsWithSocial(
		character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "群友", TextLanguage: "zh", SpeakingLanguage: "zh"},
		nil, memory.PromptWindowRecord{Revision: 1}, nil, []VisualState{{ID: "idle", Description: "待机"}}, memory.RetrievalContext{}, publicAmbientResolved(),
		SocialRespondContext{
			Intent:            &ReplyIntent{ReplyAct: "补充", Tone: "自然", RelationshipSignal: "群友", ReplyMode: "brief", Focus: "新进展", ExpressionQuery: "继续话题"},
			RecentTargetReply: "我刚才已经建议先整理项目经历。",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	last := slots[len(slots)-1]
	if last.ID != "recent_target_reply" || !last.Present || len(last.Items) != 1 {
		t.Fatalf("last slot = %#v", last)
	}
	if !strings.Contains(last.Items[0].Content, `"contextType":"recent_reply_to_same_participant"`) || !strings.Contains(last.Items[0].Content, "已经建议") {
		t.Fatalf("recent target item = %s", last.Items[0].Content)
	}
}

func TestBuildRespondContextSlotsAddsBoundedUntrustedContinuity(t *testing.T) {
	longCue := strings.Repeat("连续", 200)
	slots, err := BuildRespondContextSlotsWithSocial(
		character.Record{CharacterID: "character-1", Revision: 1, Name: "角色", TextLanguage: "zh", SpeakingLanguage: "zh"},
		nil,
		memory.PromptWindowRecord{Revision: 1}, nil,
		[]VisualState{{ID: "idle", Description: "待机"}}, memory.RetrievalContext{}, publicAmbientResolved(),
		SocialRespondContext{Intent: &ReplyIntent{ReplyMode: "normal"}, ContinuityCue: longCue, RecentFeedback: "positive"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var continuity ContextSlot
	for _, slot := range slots {
		if slot.ID == "continuity" {
			continuity = slot
		}
	}
	if !continuity.Present || continuity.Trust != "untrusted_context_data" || continuity.CachePolicy != "tail" {
		t.Fatalf("continuity slot = %#v", continuity)
	}
	if len(continuity.Items) != 1 || len([]rune(continuity.Items[0].Content)) > 600 {
		t.Fatalf("continuity payload was not bounded: %#v", continuity.Items)
	}
	if strings.Contains(continuity.Items[0].Content, "decision") {
		t.Fatalf("continuity payload exposed internal decision metadata: %s", continuity.Items[0].Content)
	}
}

func TestBuildRespondInputKeepsPersonaOutOfInstructions(t *testing.T) {
	style := "日常短句"
	name := "Rinai"
	items, err := BuildRespondInput(
		character.Record{
			CharacterID:      "character-1",
			Revision:         2,
			Name:             "亚托莉",
			Description:      "认真听用户说话。",
			DialogueStyle:    &style,
			TextLanguage:     "zh",
			SpeakingLanguage: "ja",
		},
		&profile.Snapshot{Revision: 1, PreferredName: &name},
		memory.PromptWindowRecord{Revision: 1},
		[]memory.MessageRecord{
			{Role: "user", Content: "你好", Sequence: 1},
			{Role: "assistant", Content: "我在。", Sequence: 2},
		},
		[]VisualState{{ID: "idle", Description: "待机"}, {ID: "happy", Description: "开心"}},
		memory.RetrievalContext{
			PersonalMemories: []memory.RetrievedPersonalMemory{{
				ID:                    "memory-1",
				Kind:                  "preference",
				Scope:                 memory.MemoryScope{Type: "global"},
				Content:               "喜欢安静",
				ConfidenceBasisPoints: 9000,
			}},
		},
		desktopResolved(),
	)
	if err != nil {
		t.Fatalf("BuildRespondInput() error = %v", err)
	}
	if len(items) != 8 {
		t.Fatalf("items len = %d, want 8", len(items))
	}
	if items[0].Type != model.PromptItemContextData || !strings.Contains(items[0].Content, `"contextType":"character"`) || !strings.Contains(items[0].Content, "亚托莉") {
		t.Fatalf("character context = %#v", items[0])
	}
	if !strings.Contains(items[0].Content, `"speakingLanguage":"ja"`) || !strings.Contains(items[0].Content, `"textLanguage":"zh"`) {
		t.Fatalf("character context missing languages = %#v", items[0])
	}
	if items[1].Type != model.PromptItemContextData || !strings.Contains(items[1].Content, `"contextType":"display_language"`) || !strings.Contains(items[1].Content, `"textLanguage":"zh"`) || !strings.Contains(items[1].Content, "natural Chinese") {
		t.Fatalf("display language constraint = %#v", items[1])
	}
	if items[2].Type != model.PromptItemContextData || !strings.Contains(items[2].Content, `"contextType":"user_profile"`) || !strings.Contains(items[2].Content, "Rinai") {
		t.Fatalf("profile context = %#v", items[2])
	}
	if !strings.Contains(items[3].Content, "available_visual_states") || !strings.Contains(items[3].Content, "fairy_context_data") {
		t.Fatalf("visual states = %#v", items[3])
	}
	if items[4].Type != model.PromptItemContextData || !strings.Contains(items[4].Content, `"contextType":"interaction"`) || !strings.Contains(items[4].Content, `"endpoint":"desktop"`) {
		t.Fatalf("interaction context = %#v", items[4])
	}
	if items[5].Type != model.PromptItemUserMessage || items[6].Type != model.PromptItemAssistantMessage {
		t.Fatalf("dialogue items = %#v %#v", items[5], items[6])
	}
	if !strings.Contains(items[7].Content, "retrieved_context") || !strings.Contains(items[7].Content, "喜欢安静") {
		t.Fatalf("retrieval context = %#v", items[7])
	}
	for _, forbidden := range []string{"You are FAIRY", "Stay in character", "Character name:"} {
		for _, item := range items {
			if strings.Contains(item.Content, forbidden) {
				t.Fatalf("found product-talk prompt fragment %q in %#v", forbidden, item)
			}
		}
	}
}

func TestBuildRespondInputAppliesPromptWindowSummaryAndCutoff(t *testing.T) {
	summary := "此前用户打过招呼。"
	items, err := BuildRespondInput(
		character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "认真听用户说话。", TextLanguage: "zh", SpeakingLanguage: "zh"},
		nil,
		memory.PromptWindowRecord{Revision: 2, Summary: &summary, CutoffMessageSequence: 2},
		[]memory.MessageRecord{
			{Role: "user", Content: "旧消息", Sequence: 1},
			{Role: "assistant", Content: "旧回复", Sequence: 2},
			{Role: "user", Content: "新消息", Sequence: 3},
		},
		[]VisualState{{ID: "idle", Description: "待机"}},
		memory.RetrievalContext{},
		desktopResolved(),
	)
	if err != nil {
		t.Fatalf("BuildRespondInput() error = %v", err)
	}
	joined := ""
	for _, item := range items {
		joined += item.Content + "\n"
	}
	if !strings.Contains(joined, "compaction_summary") || !strings.Contains(joined, "此前用户打过招呼。") {
		t.Fatalf("missing compaction summary: %s", joined)
	}
	if strings.Contains(joined, "旧消息") || strings.Contains(joined, "旧回复") {
		t.Fatalf("cutoff messages leaked into prompt: %s", joined)
	}
	if !strings.Contains(joined, "新消息") {
		t.Fatalf("windowed dialogue missing: %s", joined)
	}
}

func TestInstructionsForLane(t *testing.T) {
	text, tokens, err := InstructionsForLane(model.PromptLaneRespond)
	if err != nil || text != RespondInstructions || tokens != RespondMaxOutputTokens {
		t.Fatalf("respond lane = (%q, %d, %v)", text, tokens, err)
	}
	text, tokens, err = InstructionsForLane(model.PromptLaneCompact)
	if err != nil || text != CompactInstructions || tokens != CompactMaxOutputTokens {
		t.Fatalf("compact lane = (%q, %d, %v)", text, tokens, err)
	}
	text, tokens, err = InstructionsForLane(model.PromptLaneTranslate)
	if err != nil || text != TranslateInstructions || tokens != TranslateMaxOutputTokens {
		t.Fatalf("translate lane = (%q, %d, %v)", text, tokens, err)
	}
	for _, needle := range []string{"character", "dialogueStyle", "spoken", "do not invent", "do not answer as a companion"} {
		if !strings.Contains(TranslateInstructions, needle) {
			t.Fatalf("TranslateInstructions missing %q", needle)
		}
	}
	text, tokens, err = InstructionsForLane(model.PromptLaneSocialLearn)
	if err != nil || text != SocialLearnInstructions || tokens != SocialLearnMaxOutputTokens {
		t.Fatalf("social learn lane = (%q, %d, %v)", text, tokens, err)
	}
	text, tokens, err = InstructionsForLane(model.PromptLaneSocialFeedback)
	if err != nil || text != SocialFeedbackInstructions || tokens != SocialFeedbackMaxOutputTokens {
		t.Fatalf("social feedback lane = (%q, %d, %v)", text, tokens, err)
	}
}
