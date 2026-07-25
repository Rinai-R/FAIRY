package persona

import (
	"fairy/memory"
	"fairy/model"
)

func MessagesAfterCutoff(messages []memory.MessageRecord, cutoff uint64) []memory.MessageRecord {
	return messagesAfterCutoff(messages, cutoff)
}

func EncodeCompactionSummary(summary string) (model.PromptItem, error) {
	return encodeCompactionSummary(summary)
}

func EncodeSocialMemoryContext(context memory.SocialMemoryContext) (model.PromptItem, error) {
	return encodeSocialMemoryContext(context)
}

func EncodeSocialPersonNotes(notes []memory.SocialPersonNote) (model.PromptItem, error) {
	return encodeSocialPersonNotes(notes)
}

func SetContextSlotOmitReason(slots []ContextSlot, id string, reason string) {
	setContextSlotOmitReason(slots, id, reason)
}

func EncodeReplyIntentContext(intent ReplyIntent) (model.PromptItem, error) {
	return encodeReplyIntentContext(intent)
}
