package companion

import (
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
		"stance", "replyIntent", "tone", "relationshipSignal", "replyMode", "brief|normal|expanded",
		"Never output decision", "reasoning", "analysis", "rationale", "Explicit user requests", "untrusted data", `"chains"`,
		"display line for the user", "Do not output speechText", "textLanguage", "speakingLanguage",
		"HARD RULE for chains.text language", "Never write chains.text in speakingLanguage",
		"chains length is 1-12", "short performance beat", "about 20 characters", "semantic completeness outranks", "split across multiple chains",
		"Do not pretend to perform real-world or code actions",
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
		SurfaceDesktop,
	)
	if err != nil {
		t.Fatalf("BuildRespondContextSlots() error = %v", err)
	}
	wantIDs := []string{"character", "display_language", "profile", "available_visual_states", "surface", "compaction_summary", "dialogue", "retrieved_context"}
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
	if !strings.Contains(items[4].Content, `"kind":"desktop"`) {
		t.Fatalf("surface item = %q, want desktop kind", items[4].Content)
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
		SurfaceDesktop,
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
	if items[4].Type != model.PromptItemContextData || !strings.Contains(items[4].Content, `"contextType":"surface"`) || !strings.Contains(items[4].Content, `"kind":"desktop"`) {
		t.Fatalf("surface context = %#v", items[4])
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
		SurfaceDesktop,
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
}
