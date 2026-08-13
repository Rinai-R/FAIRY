package transcript

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	historyexpr "fairy/context/history/expression"
	"fairy/transport/session"
)

func validateEndpointConversationKey(characterID string, binding session.Binding, digest string) error {
	if err := ValidateID("character_id", characterID); err != nil {
		return err
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	if err := session.ValidateDigest(digest); err != nil {
		return errors.New("endpoint key digest is invalid")
	}
	return nil
}

func (s *Store) openOrCreateCharacterConversationPostgres(ctx context.Context, characterID string) (ConversationBootstrap, error) {
	if err := ValidateID("character_id", characterID); err != nil {
		return ConversationBootstrap{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return ConversationBootstrap{}, fmt.Errorf("beginning conversation transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if _, err := tx.Exec(queryCtx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", characterID); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("locking character conversation: %w", err)
	}
	conversationID, err := recentConversationID(queryCtx, tx, characterID)
	if err != nil {
		return ConversationBootstrap{}, err
	}
	if conversationID == "" {
		conversationID = newID()
		now := nowUnixMS()
		if err := insertConversationWithPromptWindow(queryCtx, tx, conversationID, characterID, now); err != nil {
			return ConversationBootstrap{}, err
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("committing conversation transaction: %w", err)
	}
	return s.loadConversationPostgres(ctx, conversationID)
}

func (s *Store) openOrCreateEndpointConversationPostgres(ctx context.Context, characterID string, binding session.Binding, digest string) (ConversationBootstrap, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return ConversationBootstrap{}, fmt.Errorf("beginning endpoint conversation transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	lockKey := characterID + "|" + string(binding.Endpoint) + "|" + digest
	if _, err := tx.Exec(queryCtx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", lockKey); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("locking endpoint conversation: %w", err)
	}

	stored, found, err := selectEndpointConversation(queryCtx, tx, characterID, binding.Endpoint, digest)
	if err != nil {
		return ConversationBootstrap{}, err
	}
	now := nowUnixMS()
	if !found {
		conversationID := newID()
		if err := insertConversationWithPromptWindow(queryCtx, tx, conversationID, characterID, now); err != nil {
			return ConversationBootstrap{}, err
		}
		if err := insertEndpointConversation(
			queryCtx, tx, characterID, binding.Endpoint, digest, conversationID,
			string(binding.Facts.Audience), string(binding.Facts.Initiation), string(binding.Facts.Presentation),
			binding.Facts.PrincipalNamespace, binding.Facts.PrincipalDigest, binding.Facts.Evaluation, now,
		); err != nil {
			return ConversationBootstrap{}, err
		}
		if err := tx.Commit(queryCtx); err != nil {
			return ConversationBootstrap{}, fmt.Errorf("committing endpoint conversation transaction: %w", err)
		}
		return s.loadConversationPostgres(ctx, conversationID)
	}
	if stored.Binding(binding.Endpoint) != binding {
		return ConversationBootstrap{}, ErrEndpointBindingMismatch
	}
	if err := touchEndpointConversation(queryCtx, tx, characterID, binding.Endpoint, digest, now); err != nil {
		return ConversationBootstrap{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("committing endpoint conversation transaction: %w", err)
	}
	return s.loadConversationPostgres(ctx, stored.ConversationID)
}

func (s *Store) lookupEndpointForConversationPostgres(ctx context.Context, conversationID string) (session.Binding, bool, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return session.Binding{}, false, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return lookupEndpointBinding(queryCtx, s.pool.Raw(), conversationID)
}

func (s *Store) loadConversationPostgres(ctx context.Context, conversationID string) (ConversationBootstrap, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return ConversationBootstrap{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return loadConversationBootstrap(queryCtx, s.pool.Raw(), conversationID)
}

func (s *Store) loadConversationRecordPostgres(ctx context.Context, conversationID string) (ConversationRecord, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return ConversationRecord{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return loadConversationRecordPostgresRow(queryCtx, s.pool.Raw(), conversationID)
}

func (s *Store) loadConversationActivityPostgres(ctx context.Context, conversationID string, nowUnixMS int64) (ConversationActivity, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return ConversationActivity{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return loadConversationActivityPostgresRow(queryCtx, s.pool.Raw(), conversationID, nowUnixMS)
}

func (s *Store) loadConversationPromptContextPostgres(ctx context.Context, conversationID string) (ConversationPromptContext, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return ConversationPromptContext{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return loadConversationPromptContextPostgresRows(queryCtx, s.pool.Raw(), conversationID)
}

func (s *Store) beginTurnPostgres(ctx context.Context, conversationID string, userMessage string, correlationMessageID string) (PersistedTurn, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return PersistedTurn{}, err
	}
	if err := ValidateContent("user message", userMessage); err != nil {
		return PersistedTurn{}, err
	}
	if err := ValidateOptionalMessageID(correlationMessageID); err != nil {
		return PersistedTurn{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return PersistedTurn{}, fmt.Errorf("beginning user message transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := requireConversationPostgres(queryCtx, tx, conversationID); err != nil {
		return PersistedTurn{}, err
	}
	turnSequence, err := nextSequencePostgres(queryCtx, tx, "conversation_turns", conversationID)
	if err != nil {
		return PersistedTurn{}, err
	}
	messageSequence, err := nextSequencePostgres(queryCtx, tx, "conversation_messages", conversationID)
	if err != nil {
		return PersistedTurn{}, err
	}
	now := nowUnixMS()
	turnID := newID()
	messageID := newID()
	if err := insertUserTurnPostgres(queryCtx, tx, turnID, conversationID, correlationMessageID, messageID, userMessage, turnSequence, messageSequence, now); err != nil {
		return PersistedTurn{}, err
	}
	if err := touchConversationPostgres(queryCtx, tx, conversationID, now); err != nil {
		return PersistedTurn{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return PersistedTurn{}, fmt.Errorf("committing user message transaction: %w", err)
	}
	return PersistedTurn{ID: turnID, ConversationID: conversationID, UserMessage: MessageRecord{
		ID: messageID, MessageID: correlationMessageID, ConversationID: conversationID, TurnID: turnID, Sequence: uint64(messageSequence),
		Role: "user", Content: userMessage, Parts: []historyexpr.Part{}, CreatedAtUnixMS: now,
	}}, nil
}

func (s *Store) beginInitiationTurnPostgres(ctx context.Context, conversationID string, evidenceIDs []string) (PersistedTurn, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return PersistedTurn{}, err
	}
	if len(evidenceIDs) == 0 || len(evidenceIDs) > 8 {
		return PersistedTurn{}, errors.New("initiation evidence count is invalid")
	}
	seenEvidence := make(map[string]struct{}, len(evidenceIDs))
	for _, evidenceID := range evidenceIDs {
		if err := ValidateEvidenceID(evidenceID); err != nil {
			return PersistedTurn{}, err
		}
		if _, exists := seenEvidence[evidenceID]; exists {
			return PersistedTurn{}, fmt.Errorf("duplicate initiation evidence %q", evidenceID)
		}
		seenEvidence[evidenceID] = struct{}{}
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return PersistedTurn{}, fmt.Errorf("beginning initiation transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := requireConversationPostgres(queryCtx, tx, conversationID); err != nil {
		return PersistedTurn{}, err
	}
	turnSequence, err := nextSequencePostgres(queryCtx, tx, "conversation_turns", conversationID)
	if err != nil {
		return PersistedTurn{}, err
	}
	now := nowUnixMS()
	turnID := newID()
	if err := insertInitiationTurnPostgres(queryCtx, tx, turnID, conversationID, turnSequence, now, evidenceIDs); err != nil {
		return PersistedTurn{}, err
	}
	if err := touchConversationPostgres(queryCtx, tx, conversationID, now); err != nil {
		return PersistedTurn{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return PersistedTurn{}, fmt.Errorf("committing initiation turn: %w", err)
	}
	return PersistedTurn{ID: turnID, ConversationID: conversationID}, nil
}

func (s *Store) completeTurnPostgres(ctx context.Context, conversationID string, turnID string, assistantMessage string) (MessageRecord, error) {
	return s.completeExpressionTurnPostgres(ctx, conversationID, turnID, assistantMessage, nil, true)
}

func (s *Store) completeExpressionTurnPostgres(ctx context.Context, conversationID string, turnID string, assistantMessage string, parts []historyexpr.Part, extractionEligible bool) (MessageRecord, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return MessageRecord{}, err
	}
	if err := ValidateID("turn_id", turnID); err != nil {
		return MessageRecord{}, err
	}
	if err := validateExpressionMessage(assistantMessage, parts); err != nil {
		return MessageRecord{}, err
	}
	storedParts := append([]historyexpr.Part{}, parts...)
	partsJSON, err := json.Marshal(storedParts)
	if err != nil {
		return MessageRecord{}, fmt.Errorf("encoding assistant expression parts: %w", err)
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return MessageRecord{}, fmt.Errorf("beginning assistant message transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := requireConversationPostgres(queryCtx, tx, conversationID); err != nil {
		return MessageRecord{}, err
	}
	now := nowUnixMS()
	messageSequence, err := nextSequencePostgres(queryCtx, tx, "conversation_messages", conversationID)
	if err != nil {
		return MessageRecord{}, err
	}
	messageID := newID()
	if err := completeTurnPostgresTx(queryCtx, tx, turnID, conversationID, messageID, assistantMessage, partsJSON, messageSequence, now, extractionEligible); err != nil {
		return MessageRecord{}, err
	}
	if err := touchConversationPostgres(queryCtx, tx, conversationID, now); err != nil {
		return MessageRecord{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return MessageRecord{}, fmt.Errorf("committing assistant message transaction: %w", err)
	}
	return MessageRecord{
		ID: messageID, ConversationID: conversationID, TurnID: turnID, Sequence: uint64(messageSequence),
		Role: "assistant", Content: assistantMessage, Parts: storedParts, CreatedAtUnixMS: now,
	}, nil
}

func (s *Store) interruptTurnPostgres(ctx context.Context, conversationID string, turnID string, publishedPrefix string) (*MessageRecord, error) {
	return s.interruptExpressionTurnPostgres(ctx, conversationID, turnID, publishedPrefix, nil)
}

func (s *Store) interruptExpressionTurnPostgres(ctx context.Context, conversationID string, turnID string, publishedPrefix string, parts []historyexpr.Part) (*MessageRecord, error) {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	if err := ValidateID("turn_id", turnID); err != nil {
		return nil, err
	}
	if publishedPrefix != "" || len(parts) > 0 {
		if err := validateExpressionMessage(publishedPrefix, parts); err != nil {
			return nil, err
		}
	}
	storedParts := append([]historyexpr.Part{}, parts...)
	partsJSON, err := json.Marshal(storedParts)
	if err != nil {
		return nil, fmt.Errorf("encoding interrupted expression parts: %w", err)
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("beginning interrupted turn transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := requireConversationPostgres(queryCtx, tx, conversationID); err != nil {
		return nil, err
	}
	now := nowUnixMS()
	if err := interruptTurnPostgresTx(queryCtx, tx, turnID, conversationID, now); err != nil {
		return nil, err
	}

	var assistant *MessageRecord
	if publishedPrefix != "" || len(parts) > 0 {
		messageSequence, err := nextSequencePostgres(queryCtx, tx, "conversation_messages", conversationID)
		if err != nil {
			return nil, err
		}
		messageID := newID()
		if err := insertAssistantMessagePostgres(queryCtx, tx, messageID, conversationID, turnID, publishedPrefix, partsJSON, messageSequence, now); err != nil {
			return nil, err
		}
		assistant = &MessageRecord{
			ID:              messageID,
			ConversationID:  conversationID,
			TurnID:          turnID,
			Sequence:        uint64(messageSequence),
			Role:            "assistant",
			Content:         publishedPrefix,
			Parts:           storedParts,
			CreatedAtUnixMS: now,
		}
	}
	if err := touchConversationPostgres(queryCtx, tx, conversationID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("committing interrupted turn transaction: %w", err)
	}
	return assistant, nil
}

func (s *Store) failTurnPostgres(ctx context.Context, conversationID string, turnID string, code string, message string, retryable bool) error {
	if err := ValidateID("conversation_id", conversationID); err != nil {
		return err
	}
	if err := ValidateID("turn_id", turnID); err != nil {
		return err
	}
	if err := ValidateContent("error code", code); err != nil {
		return err
	}
	if err := ValidateContent("error message", message); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return failTurnPostgresExec(queryCtx, s.pool.Raw(), turnID, conversationID, code, message, retryable, nowUnixMS())
}
