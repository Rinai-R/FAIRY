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

func TestRespondInstructionsMatchRustContract(t *testing.T) {
	// Exact strings from crates/fairy-harness/src/prompt_compiler.rs — migration requires byte-identical prompts.
	const rustRespond = "只输出严格 JSON object，不要 Markdown 或说明。格式：{\"chains\":[{\"visualState\":\"<available_visual_states 中的一个 id>\",\"text\":\"角色实际说出口的话\"}]}。chains 1-5段；visualState只表情绪，不输出路径/坐标/动画。读最近真实对话、当前角色设定、个人记忆和可用视觉状态，写自然下一句。记忆只作稳定偏好、关系和场景化说话方式线索；少量吸收用户常用语，不机械复读脏话或网络梗。日常口语化；普通聊天简短，强情绪先短句接住，不急着给方案。不要冒充能替用户执行现实或代码操作。不要主动提及内部能力、检索、本地层、后台任务或系统诊断，除非用户明确问系统状态。偏好称呼只是可选信息。不要分析、心理描写、动作或舞台指令。"
	const rustCompact = "FAIRY conversation compactor v2. Return only a concise plain-text summary of meaningful user and assistant dialogue for future companion turns. Exclude developer instructions, obsolete character revisions, obsolete user names, cache metadata, and duplicate canonical context. Do not invent facts or wrap the summary in JSON or Markdown."
	const rustExtract = "Read the supplied conversation batch and existing personal memories. Return exactly one JSON object: {\"mutations\": [...]}. A mutation operation is either create with kind, scope, content, confidenceBasisPoints; or supersede with memoryId plus the same fields. Use only memory IDs supplied in existingMemories. preference, profile, and experience use global scope; relationship uses the supplied current character scope. Record only durable facts directly supported by the dialogue. Return an empty mutations array when nothing should change. Do not output Markdown, reasoning, delete, or tombstone operations."
	if RespondInstructions != rustRespond {
		t.Fatalf("RespondInstructions diverged from Rust (%d vs %d runes)", utf8.RuneCountInString(RespondInstructions), utf8.RuneCountInString(rustRespond))
	}
	if CompactInstructions != rustCompact {
		t.Fatal("CompactInstructions diverged from Rust")
	}
	if ExtractInstructions != rustExtract {
		t.Fatal("ExtractInstructions diverged from Rust")
	}
	if strings.Contains(RespondInstructions, "VISUAL_STATE:") || strings.Contains(RespondInstructions, "web_search") {
		t.Fatal("forbidden protocol fragments present")
	}
}

func TestBuildRespondInputKeepsPersonaOutOfInstructions(t *testing.T) {
	style := "日常短句"
	name := "Rinai"
	items, err := BuildRespondInput(
		character.Record{
			CharacterID:   "character-1",
			Revision:      2,
			Name:          "亚托莉",
			Description:   "认真听用户说话。",
			DialogueStyle: &style,
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
	)
	if err != nil {
		t.Fatalf("BuildRespondInput() error = %v", err)
	}
	if len(items) != 6 {
		t.Fatalf("items len = %d, want 6", len(items))
	}
	if items[0].Type != model.PromptItemContextData || !strings.Contains(items[0].Content, `"contextType":"character"`) || !strings.Contains(items[0].Content, "亚托莉") {
		t.Fatalf("character context = %#v", items[0])
	}
	if items[1].Type != model.PromptItemContextData || !strings.Contains(items[1].Content, `"contextType":"user_profile"`) || !strings.Contains(items[1].Content, "Rinai") {
		t.Fatalf("profile context = %#v", items[1])
	}
	if !strings.Contains(items[2].Content, "available_visual_states") || !strings.Contains(items[2].Content, "fairy_context_data") {
		t.Fatalf("visual states = %#v", items[2])
	}
	if items[3].Type != model.PromptItemUserMessage || items[4].Type != model.PromptItemAssistantMessage {
		t.Fatalf("dialogue items = %#v %#v", items[3], items[4])
	}
	if !strings.Contains(items[5].Content, "retrieved_context") || !strings.Contains(items[5].Content, "喜欢安静") {
		t.Fatalf("retrieval context = %#v", items[5])
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
		character.Record{CharacterID: "character-1", Revision: 1, Name: "亚托莉", Description: "认真听用户说话。"},
		nil,
		memory.PromptWindowRecord{Revision: 2, Summary: &summary, CutoffMessageSequence: 2},
		[]memory.MessageRecord{
			{Role: "user", Content: "旧消息", Sequence: 1},
			{Role: "assistant", Content: "旧回复", Sequence: 2},
			{Role: "user", Content: "新消息", Sequence: 3},
		},
		[]VisualState{{ID: "idle", Description: "待机"}},
		memory.RetrievalContext{},
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
}
