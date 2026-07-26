package sociallearning

import (
	"context"

	"fairy/character"
	"fairy/config"
	domain "fairy/interaction"
	"fairy/memory"
	"fairy/model"
)

type LearningHost interface {
	ResolveInteraction(conversationID string) (domain.Resolved, error)
	LoadConversation(conversationID string) (memory.ConversationBootstrap, error)
	ActiveCharacter(characterID string) (character.Record, error)
	ModelConnection() (config.ModelConnection, error)
	ExecuteRequest(context.Context, model.CompiledPromptRequest) ([]model.StreamEvent, error)
	StoreSocialMemoryEntries(context.Context, memory.SocialMemoryBatchInput) ([]memory.SocialMemoryEntry, error)
	UpsertSocialPersonNote(context.Context, memory.SocialPersonNoteInput) (memory.SocialPersonNote, error)
	WarnLearning(conversationID string, err error)
}

type FeedbackHost interface {
	ActiveCharacter(characterID string) (character.Record, error)
	ModelConnection() (config.ModelConnection, error)
	ExecuteRequest(context.Context, model.CompiledPromptRequest) ([]model.StreamEvent, error)
	RecordSocialReplyFeedback(context.Context, memory.SocialReplyFeedbackInput) (memory.SocialReplyFeedback, error)
	WarnFeedback(conversationID, turnID string, err error)
}
