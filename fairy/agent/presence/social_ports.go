package presence

import (
	"context"

	"fairy/context/character"
	history "fairy/context/history/transcript"
	"fairy/context/social"
	"fairy/runtime/config"
	"fairy/runtime/model"
	"fairy/transport/session"
)

type LearningHost interface {
	ResolveInteraction(conversationID string) (session.Resolved, error)
	LoadConversationRecord(conversationID string) (history.ConversationRecord, error)
	ActiveCharacter(characterID string) (character.Record, error)
	ModelConnection() (config.ModelConnection, error)
	ExecuteRequest(context.Context, model.CompiledPromptRequest) ([]model.StreamEvent, error)
	StoreSocialMemoryEntries(context.Context, social.SocialMemoryBatchInput) ([]social.SocialMemoryEntry, error)
	UpsertSocialPersonNote(context.Context, social.SocialPersonNoteInput) (social.SocialPersonNote, error)
	WarnLearning(conversationID string, err error)
}

type FeedbackHost interface {
	ResolveInteraction(conversationID string) (session.Resolved, error)
	ActiveCharacter(characterID string) (character.Record, error)
	ModelConnection() (config.ModelConnection, error)
	ExecuteRequest(context.Context, model.CompiledPromptRequest) ([]model.StreamEvent, error)
	RecordSocialFeedbackBatch(context.Context, social.SocialFeedbackBatchInput) (social.SocialFeedbackBatchResult, error)
	WarnFeedback(conversationID, turnID string, err error)
}
