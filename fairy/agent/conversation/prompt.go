package conversation

import (
	"fmt"

	"fairy/agent/reply"
	"fairy/context/character"
	history "fairy/context/history/transcript"
	"fairy/context/recall"
	"fairy/context/social"
	"fairy/runtime/config"
	"fairy/runtime/model"
	"fairy/transport/session"
)

const (
	RespondInstructions               = character.RespondInstructions
	CompactInstructions               = character.CompactInstructions
	ExtractInstructions               = character.ExtractInstructions
	KnowledgeReconcileInstructions    = character.KnowledgeReconcileInstructions
	RespondMaxOutputTokens            = character.RespondMaxOutputTokens
	CompactMaxOutputTokens            = character.CompactMaxOutputTokens
	ExtractMaxOutputTokens            = character.ExtractMaxOutputTokens
	KnowledgeReconcileMaxOutputTokens = character.KnowledgeReconcileMaxOutputTokens
)

type ContextSlot = character.ContextSlot

type SocialRespondContext struct {
	Intent            *ReplyIntent
	Memory            social.SocialMemoryContext
	PersonNotes       []social.SocialPersonNote
	RecentTargetReply string
	ContinuityCue     string
	RecentFeedback    string
}

type replyDeliveryContract struct {
	MinChains              int  `json:"minChains"`
	MaxChains              int  `json:"maxChains"`
	OneConversationalHook  bool `json:"oneConversationalHook"`
	AvoidUnrequestedAdvice bool `json:"avoidUnrequestedAdvice"`
}

type replyIntentContextPayload struct {
	Delivery replyDeliveryContract `json:"delivery"`
}

func AppendDesktopInitiationContext(slots []ContextSlot, context DesktopInitiationContext) ([]ContextSlot, error) {
	return character.AppendDesktopInitiationContext(slots, character.DesktopInitiationContext{
		Trigger: context.Trigger, Activity: context.Activity, Lifecycle: context.Lifecycle,
	})
}

func BuildRespondContextSlotsWithSocial(
	record character.Record,
	userProfile *config.ProfileSnapshot,
	promptWindow history.PromptWindowRecord,
	messages []history.MessageRecord,
	states []reply.VisualState,
	retrieval recall.Context,
	resolved session.Resolved,
	social SocialRespondContext,
) ([]ContextSlot, error) {
	return character.BuildRespondContextSlotsWithSocial(
		record, userProfile, promptWindow, messages, states, retrieval, resolved,
		character.SocialRespondContext{
			Intent:            promptReplyIntent(social.Intent),
			Memory:            social.Memory,
			PersonNotes:       social.PersonNotes,
			RecentTargetReply: social.RecentTargetReply,
			ContinuityCue:     social.ContinuityCue,
			RecentFeedback:    social.RecentFeedback,
		},
	)
}

func promptReplyIntent(intent *ReplyIntent) *character.ReplyIntent {
	if intent == nil {
		return nil
	}
	return &character.ReplyIntent{
		ReplyAct: intent.ReplyAct, Tone: intent.Tone,
		RelationshipSignal: intent.RelationshipSignal, ReplyMode: intent.ReplyMode,
		Focus: intent.Focus, Avoid: append([]string(nil), intent.Avoid...),
		ReferenceInfo: intent.ReferenceInfo, DriftLevel: intent.DriftLevel,
		AnchorPolicy: intent.AnchorPolicy,
	}
}

func encodeReplyIntentContext(intent ReplyIntent) (model.PromptItem, error) {
	projected := promptReplyIntent(&intent)
	return character.EncodeReplyIntentContext(*projected)
}

func InstructionsForLane(lane model.PromptLane) (string, uint32, error) {
	switch lane {
	case model.PromptLaneRespond:
		return RespondInstructions, RespondMaxOutputTokens, nil
	case model.PromptLaneCompact:
		return CompactInstructions, CompactMaxOutputTokens, nil
	case model.PromptLaneExtract:
		return ExtractInstructions, ExtractMaxOutputTokens, nil
	case model.PromptLaneKnowledgeReconcile:
		return KnowledgeReconcileInstructions, KnowledgeReconcileMaxOutputTokens, nil
	default:
		return "", 0, fmt.Errorf("prompt lane %q is not supported", lane)
	}
}

func encodeCharacterContext(record character.Record) (model.PromptItem, error) {
	return character.EncodeCharacterContext(record)
}

func runtimeHash(value any) string { return character.RuntimeHash(value) }

func messagesAfterCutoff(messages []history.MessageRecord, cutoff uint64) []history.MessageRecord {
	return character.MessagesAfterCutoff(messages, cutoff)
}

func encodeCompactionSummary(summary string) (model.PromptItem, error) {
	return character.EncodeCompactionSummary(summary)
}

func setContextSlotOmitReason(slots []ContextSlot, id string, reason string) {
	character.SetContextSlotOmitReason(slots, id, reason)
}
