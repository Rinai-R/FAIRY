package companion

import (
	"fmt"

	"fairy/character"
	"fairy/internal/app/participation"
	"fairy/internal/app/sociallearning"
	domain "fairy/internal/domain/interaction"
	"fairy/internal/domain/persona"
	"fairy/memory"
	"fairy/model"
	"fairy/profile"
)

const (
	RespondInstructions           = persona.RespondInstructions
	CompactInstructions           = persona.CompactInstructions
	ExtractInstructions           = persona.ExtractInstructions
	TranslateInstructions         = persona.TranslateInstructions
	RespondMaxOutputTokens        = persona.RespondMaxOutputTokens
	CompactMaxOutputTokens        = persona.CompactMaxOutputTokens
	ExtractMaxOutputTokens        = persona.ExtractMaxOutputTokens
	TranslateMaxOutputTokens      = persona.TranslateMaxOutputTokens
	SocialLearnInstructions       = sociallearning.SocialLearnInstructions
	SocialLearnMaxOutputTokens    = sociallearning.SocialLearnMaxOutputTokens
	SocialFeedbackInstructions    = sociallearning.SocialFeedbackInstructions
	SocialFeedbackMaxOutputTokens = sociallearning.SocialFeedbackMaxOutputTokens
)

type ContextSlot = persona.ContextSlot

type SocialRespondContext struct {
	Intent            *ReplyIntent
	Memory            memory.SocialMemoryContext
	PersonNotes       []memory.SocialPersonNote
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

var BuildRespondInput = persona.BuildRespondInput
var BuildRespondContextSlots = persona.BuildRespondContextSlots
var PromptItemsFromContextSlots = persona.PromptItemsFromContextSlots
var BuildStablePrefixItems = persona.BuildStablePrefixItems

func AppendDesktopInitiationContext(slots []ContextSlot, context DesktopInitiationContext) ([]ContextSlot, error) {
	return persona.AppendDesktopInitiationContext(slots, persona.DesktopInitiationContext{
		Trigger: context.Trigger, Activity: context.Activity, Lifecycle: context.Lifecycle,
	})
}

func BuildRespondContextSlotsWithSocial(
	record character.Record,
	userProfile *profile.Snapshot,
	promptWindow memory.PromptWindowRecord,
	messages []memory.MessageRecord,
	states []VisualState,
	retrieval memory.RetrievalContext,
	resolved domain.Resolved,
	social SocialRespondContext,
) ([]ContextSlot, error) {
	return persona.BuildRespondContextSlotsWithSocial(
		record, userProfile, promptWindow, messages, states, retrieval, resolved,
		persona.SocialRespondContext{
			Intent:            promptReplyIntent(social.Intent),
			Memory:            social.Memory,
			PersonNotes:       social.PersonNotes,
			RecentTargetReply: social.RecentTargetReply,
			ContinuityCue:     social.ContinuityCue,
			RecentFeedback:    social.RecentFeedback,
		},
	)
}

func promptReplyIntent(intent *ReplyIntent) *persona.ReplyIntent {
	if intent == nil {
		return nil
	}
	return &persona.ReplyIntent{
		ReplyAct: intent.ReplyAct, Tone: intent.Tone,
		RelationshipSignal: intent.RelationshipSignal, ReplyMode: intent.ReplyMode,
		Focus: intent.Focus, Avoid: append([]string(nil), intent.Avoid...),
		ReferenceInfo: intent.ReferenceInfo, DriftLevel: intent.DriftLevel,
		AnchorPolicy: intent.AnchorPolicy,
	}
}

func encodeReplyIntentContext(intent ReplyIntent) (model.PromptItem, error) {
	projected := promptReplyIntent(&intent)
	return persona.EncodeReplyIntentContext(*projected)
}

func InstructionsForLane(lane model.PromptLane) (string, uint32, error) {
	switch lane {
	case model.PromptLaneRespond:
		return RespondInstructions, RespondMaxOutputTokens, nil
	case model.PromptLaneParticipate:
		return participation.ParticipationInstructions, participation.ParticipationMaxOutputTokens, nil
	case model.PromptLaneCompact:
		return CompactInstructions, CompactMaxOutputTokens, nil
	case model.PromptLaneExtract:
		return ExtractInstructions, ExtractMaxOutputTokens, nil
	case model.PromptLaneTranslate:
		return TranslateInstructions, TranslateMaxOutputTokens, nil
	case model.PromptLaneSocialLearn:
		return sociallearning.SocialLearnInstructions, sociallearning.SocialLearnMaxOutputTokens, nil
	case model.PromptLaneSocialFeedback:
		return sociallearning.SocialFeedbackInstructions, sociallearning.SocialFeedbackMaxOutputTokens, nil
	default:
		return "", 0, fmt.Errorf("prompt lane %q is not supported", lane)
	}
}

func encodeCharacterContext(record character.Record) (model.PromptItem, error) {
	return persona.EncodeCharacterContext(record)
}

func runtimeHash(value any) string { return persona.RuntimeHash(value) }

func messagesAfterCutoff(messages []memory.MessageRecord, cutoff uint64) []memory.MessageRecord {
	return persona.MessagesAfterCutoff(messages, cutoff)
}

func encodeCompactionSummary(summary string) (model.PromptItem, error) {
	return persona.EncodeCompactionSummary(summary)
}

func encodeSocialMemoryContext(context memory.SocialMemoryContext) (model.PromptItem, error) {
	return persona.EncodeSocialMemoryContext(context)
}

func encodeSocialPersonNotes(notes []memory.SocialPersonNote) (model.PromptItem, error) {
	return persona.EncodeSocialPersonNotes(notes)
}

func setContextSlotOmitReason(slots []ContextSlot, id string, reason string) {
	persona.SetContextSlotOmitReason(slots, id, reason)
}
