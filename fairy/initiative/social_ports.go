package initiative

import (
	"context"

	"fairy/character"
	"fairy/config"
	"fairy/memory"
	"fairy/model"
	"fairy/session"
)

type LearningHost interface {
	ResolveInteraction(conversationID string) (session.Resolved, error)
	LoadConversationRecord(conversationID string) (memory.ConversationRecord, error)
	ActiveCharacter(characterID string) (character.Record, error)
	ModelConnection() (config.ModelConnection, error)
	ExecuteRequest(context.Context, model.CompiledPromptRequest) ([]model.StreamEvent, error)
	StoreSocialMemoryEntries(context.Context, memory.SocialMemoryBatchInput) ([]memory.SocialMemoryEntry, error)
	UpsertSocialPersonNote(context.Context, memory.SocialPersonNoteInput) (memory.SocialPersonNote, error)
	WarnLearning(conversationID string, err error)
}

type FeedbackHost interface {
	ResolveInteraction(conversationID string) (session.Resolved, error)
	ActiveCharacter(characterID string) (character.Record, error)
	ModelConnection() (config.ModelConnection, error)
	ExecuteRequest(context.Context, model.CompiledPromptRequest) ([]model.StreamEvent, error)
	RecordSocialFeedbackBatch(context.Context, memory.SocialFeedbackBatchInput) (memory.SocialFeedbackBatchResult, error)
	WarnFeedback(conversationID, turnID string, err error)
}
