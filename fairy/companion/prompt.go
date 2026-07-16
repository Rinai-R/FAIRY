package companion

import (
	"encoding/json"
	"fmt"

	"fairy/character"
	"fairy/memory"
	"fairy/model"
	"fairy/profile"
)

// Stable lane instructions must match the Rust PromptCompiler contract.
const RespondInstructions = "只输出严格 JSON object，不要 Markdown 或说明。格式：{\"chains\":[{\"visualState\":\"<available_visual_states 中的一个 id>\",\"text\":\"角色实际说出口的话\"}]}。chains 1-5段；visualState只表情绪，不输出路径/坐标/动画。读最近真实对话、当前角色设定、个人记忆和可用视觉状态，写自然下一句。记忆只作稳定偏好、关系和场景化说话方式线索；少量吸收用户常用语，不机械复读脏话或网络梗。日常口语化；普通聊天简短，强情绪先短句接住，不急着给方案。不要冒充能替用户执行现实或代码操作。不要主动提及内部能力、检索、本地层、后台任务或系统诊断，除非用户明确问系统状态。偏好称呼只是可选信息。不要分析、心理描写、动作或舞台指令。"

const CompactInstructions = "FAIRY conversation compactor v2. Return only a concise plain-text summary of meaningful user and assistant dialogue for future companion turns. Exclude developer instructions, obsolete character revisions, obsolete user names, cache metadata, and duplicate canonical context. Do not invent facts or wrap the summary in JSON or Markdown."

const ExtractInstructions = "Read the supplied conversation batch and existing personal memories. Return exactly one JSON object: {\"mutations\": [...]}. A mutation operation is either create with kind, scope, content, confidenceBasisPoints; or supersede with memoryId plus the same fields. Use only memory IDs supplied in existingMemories. preference, profile, and experience use global scope; relationship uses the supplied current character scope. Record only durable facts directly supported by the dialogue. Return an empty mutations array when nothing should change. Do not output Markdown, reasoning, delete, or tombstone operations."

const RespondMaxOutputTokens uint32 = 640
const CompactMaxOutputTokens uint32 = 640
const ExtractMaxOutputTokens uint32 = 800

type characterContextPayload struct {
	ContextType   string  `json:"contextType"`
	Revision      uint64  `json:"revision"`
	Name          string  `json:"name"`
	Description   string  `json:"description"`
	DialogueStyle *string `json:"dialogueStyle,omitempty"`
}

type userProfileContextPayload struct {
	ContextType   string  `json:"contextType"`
	Revision      *uint64 `json:"revision"`
	PreferredName *string `json:"preferredName"`
}

type visualStateEntry struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type availableVisualStatesPayload struct {
	Type   string             `json:"type"`
	States []visualStateEntry `json:"states"`
}

type retrievedContextPayload struct {
	Type    string                  `json:"type"`
	Context memory.RetrievalContext `json:"context"`
}

type fairyContextEnvelope struct {
	FairyContextData any `json:"fairy_context_data"`
}

// BuildRespondInput assembles cache-friendly prompt items.
// Character, profile, visual states and retrieval stay quoted user data — never system instructions.
// Prompt window summary/cutoff shrinks the dialogue window without rewriting persisted messages.
func BuildRespondInput(
	record character.Record,
	userProfile *profile.Snapshot,
	promptWindow memory.PromptWindowRecord,
	messages []memory.MessageRecord,
	states []VisualState,
	retrieval memory.RetrievalContext,
) ([]model.PromptItem, error) {
	windowed := messagesAfterCutoff(messages, promptWindow.CutoffMessageSequence)
	items := make([]model.PromptItem, 0, len(windowed)+5)
	characterItem, err := encodeCharacterContext(record)
	if err != nil {
		return nil, err
	}
	items = append(items, characterItem)
	profileItem, err := encodeUserProfileContext(userProfile)
	if err != nil {
		return nil, err
	}
	items = append(items, profileItem)
	// Match Rust stable_visual_context_index: visual sits after character/profile, before summary/dialogue.
	visualItem, err := encodeAvailableVisualStates(states)
	if err != nil {
		return nil, err
	}
	items = append(items, visualItem)
	if promptWindow.Summary != nil && *promptWindow.Summary != "" {
		summaryItem, err := encodeCompactionSummary(*promptWindow.Summary)
		if err != nil {
			return nil, err
		}
		items = append(items, summaryItem)
	}
	for _, message := range windowed {
		switch message.Role {
		case "user":
			items = append(items, model.PromptItem{Type: model.PromptItemUserMessage, Content: message.Content})
		case "assistant":
			items = append(items, model.PromptItem{Type: model.PromptItemAssistantMessage, Content: message.Content})
		}
	}
	if !retrieval.Empty() {
		retrievalItem, err := encodeRetrievedContext(retrieval)
		if err != nil {
			return nil, err
		}
		items = append(items, retrievalItem)
	}
	return items, nil
}

func messagesAfterCutoff(messages []memory.MessageRecord, cutoff uint64) []memory.MessageRecord {
	if cutoff == 0 {
		return messages
	}
	windowed := make([]memory.MessageRecord, 0, len(messages))
	for _, message := range messages {
		if message.Sequence > cutoff {
			windowed = append(windowed, message)
		}
	}
	return windowed
}

func encodeCharacterContext(record character.Record) (model.PromptItem, error) {
	payload, err := json.Marshal(characterContextPayload{
		ContextType:   "character",
		Revision:      record.Revision,
		Name:          record.Name,
		Description:   record.Description,
		DialogueStyle: record.DialogueStyle,
	})
	if err != nil {
		return model.PromptItem{}, fmt.Errorf("serializing character context: %w", err)
	}
	return model.PromptItem{Type: model.PromptItemContextData, Content: string(payload)}, nil
}

func encodeUserProfileContext(snapshot *profile.Snapshot) (model.PromptItem, error) {
	var revision *uint64
	var preferredName *string
	if snapshot != nil {
		value := snapshot.Revision
		revision = &value
		preferredName = snapshot.PreferredName
	}
	payload, err := json.Marshal(userProfileContextPayload{
		ContextType:   "user_profile",
		Revision:      revision,
		PreferredName: preferredName,
	})
	if err != nil {
		return model.PromptItem{}, fmt.Errorf("serializing user profile context: %w", err)
	}
	return model.PromptItem{Type: model.PromptItemContextData, Content: string(payload)}, nil
}

func encodeAvailableVisualStates(states []VisualState) (model.PromptItem, error) {
	entries := make([]visualStateEntry, 0, len(states))
	for _, state := range states {
		entries = append(entries, visualStateEntry{ID: state.ID, Description: state.Description})
	}
	payload, err := json.Marshal(fairyContextEnvelope{
		FairyContextData: availableVisualStatesPayload{
			Type:   "available_visual_states",
			States: entries,
		},
	})
	if err != nil {
		return model.PromptItem{}, fmt.Errorf("serializing available visual states: %w", err)
	}
	return model.PromptItem{Type: model.PromptItemContextData, Content: string(payload)}, nil
}

func encodeRetrievedContext(context memory.RetrievalContext) (model.PromptItem, error) {
	payload, err := json.Marshal(fairyContextEnvelope{
		FairyContextData: retrievedContextPayload{
			Type:    "retrieved_context",
			Context: context,
		},
	})
	if err != nil {
		return model.PromptItem{}, fmt.Errorf("serializing retrieved context: %w", err)
	}
	return model.PromptItem{Type: model.PromptItemContextData, Content: string(payload)}, nil
}

func encodeCompactionSummary(summary string) (model.PromptItem, error) {
	payload, err := json.Marshal(fairyContextEnvelope{
		FairyContextData: map[string]string{
			"type":    "compaction_summary",
			"summary": summary,
		},
	})
	if err != nil {
		return model.PromptItem{}, fmt.Errorf("serializing compaction summary: %w", err)
	}
	return model.PromptItem{Type: model.PromptItemContextData, Content: string(payload)}, nil
}

func InstructionsForLane(lane model.PromptLane) (string, uint32, error) {
	switch lane {
	case model.PromptLaneRespond:
		return RespondInstructions, RespondMaxOutputTokens, nil
	case model.PromptLaneCompact:
		return CompactInstructions, CompactMaxOutputTokens, nil
	case model.PromptLaneExtract:
		return ExtractInstructions, ExtractMaxOutputTokens, nil
	default:
		return "", 0, fmt.Errorf("prompt lane %q is not supported", lane)
	}
}
