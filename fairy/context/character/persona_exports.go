package character

import (
	history "fairy/context/history/transcript"
	"fairy/context/social"
	"fairy/runtime/model"
)

func MessagesAfterCutoff(messages []history.MessageRecord, cutoff uint64) []history.MessageRecord {
	return messagesAfterCutoff(messages, cutoff)
}

func EncodeCompactionSummary(summary string) (model.PromptItem, error) {
	return encodeCompactionSummary(summary)
}

func EncodeSocialMemoryContext(context social.SocialMemoryContext) (model.PromptItem, error) {
	return encodeSocialMemoryContext(context)
}

func EncodeSocialPersonNotes(notes []social.SocialPersonNote) (model.PromptItem, error) {
	return encodeSocialPersonNotes(notes)
}

func SetContextSlotOmitReason(slots []ContextSlot, id string, reason string) {
	setContextSlotOmitReason(slots, id, reason)
}

func EncodeReplyIntentContext(intent ReplyIntent) (model.PromptItem, error) {
	return encodeReplyIntentContext(intent)
}
