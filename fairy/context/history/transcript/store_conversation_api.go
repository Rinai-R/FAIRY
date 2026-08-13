package transcript

import (
	"context"

	historyexpr "fairy/context/history/expression"
	"fairy/transport/session"
)

func (s *Store) BeginInitiationTurn(conversationID string, evidenceIDs []string) (PersistedTurn, error) {
	return s.BeginInitiationTurnContext(context.Background(), conversationID, evidenceIDs)
}

func (s *Store) BeginInitiationTurnContext(ctx context.Context, conversationID string, evidenceIDs []string) (PersistedTurn, error) {
	if s.usesSeekDB() {
		return s.beginInitiationTurnSeekDB(ctx, conversationID, evidenceIDs)
	}
	if !s.usesPostgres() {
		return PersistedTurn{}, ErrStoreBackendUnavailable
	}
	return s.beginInitiationTurnPostgres(ctx, conversationID, evidenceIDs)
}

func (s *Store) OpenOrCreateEndpointConversation(characterID string, binding session.Binding, endpointKeyDigest string) (ConversationBootstrap, error) {
	return s.OpenOrCreateEndpointConversationContext(context.Background(), characterID, binding, endpointKeyDigest)
}

func (s *Store) OpenOrCreateEndpointConversationContext(ctx context.Context, characterID string, binding session.Binding, endpointKeyDigest string) (ConversationBootstrap, error) {
	if err := validateEndpointConversationKey(characterID, binding, endpointKeyDigest); err != nil {
		return ConversationBootstrap{}, err
	}
	if s.usesSeekDB() {
		return s.openOrCreateEndpointConversationSeekDB(ctx, characterID, binding, endpointKeyDigest)
	}
	if !s.usesPostgres() {
		return ConversationBootstrap{}, ErrStoreBackendUnavailable
	}
	return s.openOrCreateEndpointConversationPostgres(ctx, characterID, binding, endpointKeyDigest)
}

func (s *Store) LookupEndpointForConversation(conversationID string) (session.Binding, bool, error) {
	return s.LookupEndpointForConversationContext(context.Background(), conversationID)
}

func (s *Store) LookupEndpointForConversationContext(ctx context.Context, conversationID string) (session.Binding, bool, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return session.Binding{}, false, err
	}
	if s.usesSeekDB() {
		return s.lookupEndpointForConversationSeekDB(ctx, conversationID)
	}
	if !s.usesPostgres() {
		return session.Binding{}, false, ErrStoreBackendUnavailable
	}
	return s.lookupEndpointForConversationPostgres(ctx, conversationID)
}

func (s *Store) OpenOrCreateCharacterConversation(characterID string) (ConversationBootstrap, error) {
	return s.OpenOrCreateCharacterConversationContext(context.Background(), characterID)
}

func (s *Store) OpenOrCreateCharacterConversationContext(ctx context.Context, characterID string) (ConversationBootstrap, error) {
	if err := ValidateID("character_id", characterID); err != nil {
		return ConversationBootstrap{}, err
	}
	if s.usesSeekDB() {
		return s.openOrCreateCharacterConversationSeekDB(ctx, characterID)
	}
	if !s.usesPostgres() {
		return ConversationBootstrap{}, ErrStoreBackendUnavailable
	}
	return s.openOrCreateCharacterConversationPostgres(ctx, characterID)
}

func (s *Store) LoadConversation(conversationID string) (ConversationBootstrap, error) {
	return s.LoadConversationContext(context.Background(), conversationID)
}

func (s *Store) LoadConversationContext(ctx context.Context, conversationID string) (ConversationBootstrap, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return ConversationBootstrap{}, err
	}
	if s.usesSeekDB() {
		return s.loadConversationSeekDB(ctx, conversationID)
	}
	if !s.usesPostgres() {
		return ConversationBootstrap{}, ErrStoreBackendUnavailable
	}
	return s.loadConversationPostgres(ctx, conversationID)
}

func (s *Store) LoadConversationRecord(conversationID string) (ConversationRecord, error) {
	return s.LoadConversationRecordContext(context.Background(), conversationID)
}

func (s *Store) LoadConversationRecordContext(ctx context.Context, conversationID string) (ConversationRecord, error) {
	if s.usesSeekDB() {
		return ConversationRecord{}, ErrSeekDBOperationPending
	}
	if !s.usesPostgres() {
		return ConversationRecord{}, ErrStoreBackendUnavailable
	}
	return s.loadConversationRecordPostgres(ctx, conversationID)
}

func (s *Store) LoadConversationActivity(conversationID string, nowUnixMS int64) (ConversationActivity, error) {
	return s.LoadConversationActivityContext(context.Background(), conversationID, nowUnixMS)
}

func (s *Store) LoadConversationActivityContext(ctx context.Context, conversationID string, nowUnixMS int64) (ConversationActivity, error) {
	if s.usesSeekDB() {
		return ConversationActivity{}, ErrSeekDBOperationPending
	}
	if !s.usesPostgres() {
		return ConversationActivity{}, ErrStoreBackendUnavailable
	}
	return s.loadConversationActivityPostgres(ctx, conversationID, nowUnixMS)
}

func (s *Store) LoadConversationPrompt(conversationID string) (ConversationPromptContext, error) {
	return s.LoadConversationPromptContext(context.Background(), conversationID)
}

func (s *Store) LoadConversationPromptContext(ctx context.Context, conversationID string) (ConversationPromptContext, error) {
	if s.usesSeekDB() {
		return ConversationPromptContext{}, ErrSeekDBOperationPending
	}
	if !s.usesPostgres() {
		return ConversationPromptContext{}, ErrStoreBackendUnavailable
	}
	return s.loadConversationPromptContextPostgres(ctx, conversationID)
}

func (s *Store) BeginTurn(conversationID string, userMessage string) (PersistedTurn, error) {
	return s.BeginTurnContext(context.Background(), conversationID, userMessage)
}

func (s *Store) BeginTurnContext(ctx context.Context, conversationID string, userMessage string) (PersistedTurn, error) {
	if s.usesSeekDB() {
		return s.beginTurnSeekDB(ctx, conversationID, userMessage, "")
	}
	if !s.usesPostgres() {
		return PersistedTurn{}, ErrStoreBackendUnavailable
	}
	return s.beginTurnPostgres(ctx, conversationID, userMessage, "")
}

func (s *Store) BeginCorrelatedTurn(conversationID string, userMessage string, messageID string) (PersistedTurn, error) {
	return s.BeginCorrelatedTurnContext(context.Background(), conversationID, userMessage, messageID)
}

func (s *Store) BeginCorrelatedTurnContext(ctx context.Context, conversationID string, userMessage string, messageID string) (PersistedTurn, error) {
	if s.usesSeekDB() {
		return s.beginTurnSeekDB(ctx, conversationID, userMessage, messageID)
	}
	if !s.usesPostgres() {
		return PersistedTurn{}, ErrStoreBackendUnavailable
	}
	return s.beginTurnPostgres(ctx, conversationID, userMessage, messageID)
}

func (s *Store) CompleteTurn(conversationID string, turnID string, assistantMessage string) (MessageRecord, error) {
	return s.CompleteTurnContext(context.Background(), conversationID, turnID, assistantMessage)
}

func (s *Store) CompleteTurnContext(ctx context.Context, conversationID string, turnID string, assistantMessage string) (MessageRecord, error) {
	if s.usesSeekDB() {
		return s.completeExpressionTurnSeekDB(ctx, conversationID, turnID, assistantMessage, nil, true)
	}
	if !s.usesPostgres() {
		return MessageRecord{}, ErrStoreBackendUnavailable
	}
	return s.completeTurnPostgres(ctx, conversationID, turnID, assistantMessage)
}

func (s *Store) CompleteExpressionTurn(conversationID string, turnID string, assistantMessage string, parts []historyexpr.Part) (MessageRecord, error) {
	return s.CompleteExpressionTurnContext(context.Background(), conversationID, turnID, assistantMessage, parts)
}

func (s *Store) CompleteExpressionTurnContext(ctx context.Context, conversationID string, turnID string, assistantMessage string, parts []historyexpr.Part) (MessageRecord, error) {
	if s.usesSeekDB() {
		return s.completeExpressionTurnSeekDB(ctx, conversationID, turnID, assistantMessage, parts, true)
	}
	if !s.usesPostgres() {
		return MessageRecord{}, ErrStoreBackendUnavailable
	}
	return s.completeExpressionTurnPostgres(ctx, conversationID, turnID, assistantMessage, parts, true)
}

func (s *Store) CompleteExpressionTurnForPolicy(conversationID string, turnID string, assistantMessage string, parts []historyexpr.Part, extractionEligible bool) (MessageRecord, error) {
	if s.usesSeekDB() {
		return s.completeExpressionTurnSeekDB(context.Background(), conversationID, turnID, assistantMessage, parts, extractionEligible)
	}
	if !s.usesPostgres() {
		return MessageRecord{}, ErrStoreBackendUnavailable
	}
	return s.completeExpressionTurnPostgres(context.Background(), conversationID, turnID, assistantMessage, parts, extractionEligible)
}

func (s *Store) InterruptTurn(conversationID string, turnID string, publishedPrefix string) (*MessageRecord, error) {
	return s.InterruptTurnContext(context.Background(), conversationID, turnID, publishedPrefix)
}

func (s *Store) InterruptTurnContext(ctx context.Context, conversationID string, turnID string, publishedPrefix string) (*MessageRecord, error) {
	if s.usesSeekDB() {
		return s.interruptExpressionTurnSeekDB(ctx, conversationID, turnID, publishedPrefix, nil)
	}
	if !s.usesPostgres() {
		return nil, ErrStoreBackendUnavailable
	}
	return s.interruptTurnPostgres(ctx, conversationID, turnID, publishedPrefix)
}

func (s *Store) InterruptExpressionTurn(conversationID string, turnID string, publishedPrefix string, parts []historyexpr.Part) (*MessageRecord, error) {
	return s.InterruptExpressionTurnContext(context.Background(), conversationID, turnID, publishedPrefix, parts)
}

func (s *Store) InterruptExpressionTurnContext(ctx context.Context, conversationID string, turnID string, publishedPrefix string, parts []historyexpr.Part) (*MessageRecord, error) {
	if s.usesSeekDB() {
		return s.interruptExpressionTurnSeekDB(ctx, conversationID, turnID, publishedPrefix, parts)
	}
	if !s.usesPostgres() {
		return nil, ErrStoreBackendUnavailable
	}
	return s.interruptExpressionTurnPostgres(ctx, conversationID, turnID, publishedPrefix, parts)
}

func (s *Store) FailTurn(conversationID string, turnID string, code string, message string, retryable bool) error {
	return s.FailTurnContext(context.Background(), conversationID, turnID, code, message, retryable)
}

func (s *Store) FailTurnContext(ctx context.Context, conversationID string, turnID string, code string, message string, retryable bool) error {
	if s.usesSeekDB() {
		return s.failTurnSeekDB(ctx, conversationID, turnID, code, message, retryable)
	}
	if !s.usesPostgres() {
		return ErrStoreBackendUnavailable
	}
	return s.failTurnPostgres(ctx, conversationID, turnID, code, message, retryable)
}
