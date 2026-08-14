package transcript

import (
	"context"
	"errors"

	historyexpr "fairy/context/history/expression"
	"fairy/transport/session"
)

func (s *Store) BeginInitiationTurn(conversationID string, evidenceIDs []string) (PersistedTurn, error) {
	return s.BeginInitiationTurnContext(context.Background(), conversationID, evidenceIDs)
}

func (s *Store) BeginInitiationTurnContext(ctx context.Context, conversationID string, evidenceIDs []string) (PersistedTurn, error) {
	if !s.usesSeekDB() {
		return PersistedTurn{}, ErrStoreBackendUnavailable
	}
	return s.beginInitiationTurnSeekDB(ctx, conversationID, evidenceIDs)
}

func (s *Store) OpenOrCreateEndpointConversation(characterID string, binding session.Binding, endpointKeyDigest string) (ConversationBootstrap, error) {
	return s.OpenOrCreateEndpointConversationContext(context.Background(), characterID, binding, endpointKeyDigest)
}

func (s *Store) OpenOrCreateEndpointConversationContext(ctx context.Context, characterID string, binding session.Binding, endpointKeyDigest string) (ConversationBootstrap, error) {
	if err := validateEndpointConversationKey(characterID, binding, endpointKeyDigest); err != nil {
		return ConversationBootstrap{}, err
	}
	if !s.usesSeekDB() {
		return ConversationBootstrap{}, ErrStoreBackendUnavailable
	}
	return s.openOrCreateEndpointConversationSeekDB(ctx, characterID, binding, endpointKeyDigest)
}

func (s *Store) LookupEndpointForConversation(conversationID string) (session.Binding, bool, error) {
	return s.LookupEndpointForConversationContext(context.Background(), conversationID)
}

func (s *Store) LookupEndpointForConversationContext(ctx context.Context, conversationID string) (session.Binding, bool, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return session.Binding{}, false, err
	}
	if !s.usesSeekDB() {
		return session.Binding{}, false, ErrStoreBackendUnavailable
	}
	return s.lookupEndpointForConversationSeekDB(ctx, conversationID)
}

func (s *Store) OpenOrCreateCharacterConversation(characterID string) (ConversationBootstrap, error) {
	return s.OpenOrCreateCharacterConversationContext(context.Background(), characterID)
}

func (s *Store) OpenOrCreateCharacterConversationContext(ctx context.Context, characterID string) (ConversationBootstrap, error) {
	if err := ValidateID("character_id", characterID); err != nil {
		return ConversationBootstrap{}, err
	}
	if !s.usesSeekDB() {
		return ConversationBootstrap{}, ErrStoreBackendUnavailable
	}
	return s.openOrCreateCharacterConversationSeekDB(ctx, characterID)
}

func (s *Store) LoadConversation(conversationID string) (ConversationBootstrap, error) {
	return s.LoadConversationContext(context.Background(), conversationID)
}

func (s *Store) LoadConversationContext(ctx context.Context, conversationID string) (ConversationBootstrap, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return ConversationBootstrap{}, err
	}
	if !s.usesSeekDB() {
		return ConversationBootstrap{}, ErrStoreBackendUnavailable
	}
	return s.loadConversationSeekDB(ctx, conversationID)
}

func (s *Store) LoadConversationRecord(conversationID string) (ConversationRecord, error) {
	return s.LoadConversationRecordContext(context.Background(), conversationID)
}

func (s *Store) LoadConversationRecordContext(ctx context.Context, conversationID string) (ConversationRecord, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return ConversationRecord{}, err
	}
	if !s.usesSeekDB() {
		return ConversationRecord{}, ErrStoreBackendUnavailable
	}
	return s.loadConversationRecordSeekDB(ctx, conversationID)
}

func (s *Store) LoadConversationActivity(conversationID string, nowUnixMS int64) (ConversationActivity, error) {
	return s.LoadConversationActivityContext(context.Background(), conversationID, nowUnixMS)
}

func (s *Store) LoadConversationActivityContext(ctx context.Context, conversationID string, nowUnixMS int64) (ConversationActivity, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return ConversationActivity{}, err
	}
	if nowUnixMS <= 0 {
		return ConversationActivity{}, errors.New("activity evaluation time must be positive")
	}
	if !s.usesSeekDB() {
		return ConversationActivity{}, ErrStoreBackendUnavailable
	}
	return s.loadConversationActivitySeekDB(ctx, conversationID, nowUnixMS)
}

func (s *Store) LoadConversationPrompt(conversationID string) (ConversationPromptContext, error) {
	return s.LoadConversationPromptContext(context.Background(), conversationID)
}

func (s *Store) LoadConversationPromptContext(ctx context.Context, conversationID string) (ConversationPromptContext, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return ConversationPromptContext{}, err
	}
	if !s.usesSeekDB() {
		return ConversationPromptContext{}, ErrStoreBackendUnavailable
	}
	return s.loadConversationPromptContextSeekDB(ctx, conversationID)
}

func (s *Store) BeginTurn(conversationID string, userMessage string) (PersistedTurn, error) {
	return s.BeginTurnContext(context.Background(), conversationID, userMessage)
}

func (s *Store) BeginTurnContext(ctx context.Context, conversationID string, userMessage string) (PersistedTurn, error) {
	if !s.usesSeekDB() {
		return PersistedTurn{}, ErrStoreBackendUnavailable
	}
	return s.beginTurnSeekDB(ctx, conversationID, userMessage, "")
}

func (s *Store) BeginCorrelatedTurn(conversationID string, userMessage string, messageID string) (PersistedTurn, error) {
	return s.BeginCorrelatedTurnContext(context.Background(), conversationID, userMessage, messageID)
}

func (s *Store) BeginCorrelatedTurnContext(ctx context.Context, conversationID string, userMessage string, messageID string) (PersistedTurn, error) {
	if !s.usesSeekDB() {
		return PersistedTurn{}, ErrStoreBackendUnavailable
	}
	return s.beginTurnSeekDB(ctx, conversationID, userMessage, messageID)
}

func (s *Store) CompleteTurn(conversationID string, turnID string, assistantMessage string) (MessageRecord, error) {
	return s.CompleteTurnContext(context.Background(), conversationID, turnID, assistantMessage)
}

func (s *Store) CompleteTurnContext(ctx context.Context, conversationID string, turnID string, assistantMessage string) (MessageRecord, error) {
	if !s.usesSeekDB() {
		return MessageRecord{}, ErrStoreBackendUnavailable
	}
	return s.completeExpressionTurnSeekDB(ctx, conversationID, turnID, assistantMessage, nil, true)
}

func (s *Store) CompleteExpressionTurn(conversationID string, turnID string, assistantMessage string, parts []historyexpr.Part) (MessageRecord, error) {
	return s.CompleteExpressionTurnContext(context.Background(), conversationID, turnID, assistantMessage, parts)
}

func (s *Store) CompleteExpressionTurnContext(ctx context.Context, conversationID string, turnID string, assistantMessage string, parts []historyexpr.Part) (MessageRecord, error) {
	if !s.usesSeekDB() {
		return MessageRecord{}, ErrStoreBackendUnavailable
	}
	return s.completeExpressionTurnSeekDB(ctx, conversationID, turnID, assistantMessage, parts, true)
}

func (s *Store) CompleteExpressionTurnForPolicy(conversationID string, turnID string, assistantMessage string, parts []historyexpr.Part, extractionEligible bool) (MessageRecord, error) {
	if !s.usesSeekDB() {
		return MessageRecord{}, ErrStoreBackendUnavailable
	}
	return s.completeExpressionTurnSeekDB(context.Background(), conversationID, turnID, assistantMessage, parts, extractionEligible)
}

func (s *Store) InterruptTurn(conversationID string, turnID string, publishedPrefix string) (*MessageRecord, error) {
	return s.InterruptTurnContext(context.Background(), conversationID, turnID, publishedPrefix)
}

func (s *Store) InterruptTurnContext(ctx context.Context, conversationID string, turnID string, publishedPrefix string) (*MessageRecord, error) {
	if !s.usesSeekDB() {
		return nil, ErrStoreBackendUnavailable
	}
	return s.interruptExpressionTurnSeekDB(ctx, conversationID, turnID, publishedPrefix, nil)
}

func (s *Store) InterruptExpressionTurn(conversationID string, turnID string, publishedPrefix string, parts []historyexpr.Part) (*MessageRecord, error) {
	return s.InterruptExpressionTurnContext(context.Background(), conversationID, turnID, publishedPrefix, parts)
}

func (s *Store) InterruptExpressionTurnContext(ctx context.Context, conversationID string, turnID string, publishedPrefix string, parts []historyexpr.Part) (*MessageRecord, error) {
	if !s.usesSeekDB() {
		return nil, ErrStoreBackendUnavailable
	}
	return s.interruptExpressionTurnSeekDB(ctx, conversationID, turnID, publishedPrefix, parts)
}

func (s *Store) FailTurn(conversationID string, turnID string, code string, message string, retryable bool) error {
	return s.FailTurnContext(context.Background(), conversationID, turnID, code, message, retryable)
}

func (s *Store) FailTurnContext(ctx context.Context, conversationID string, turnID string, code string, message string, retryable bool) error {
	if !s.usesSeekDB() {
		return ErrStoreBackendUnavailable
	}
	return s.failTurnSeekDB(ctx, conversationID, turnID, code, message, retryable)
}
