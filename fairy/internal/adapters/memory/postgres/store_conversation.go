package postgres

import (
	"context"
	"errors"
	"fmt"

	contracts "fairy/contracts/interaction"
	domainmemory "fairy/internal/domain/memory"

	"github.com/jackc/pgx/v5"
)

func (s *Store) openOrCreateCharacterConversationPostgres(ctx context.Context, characterID string) (ConversationBootstrap, error) {
	if err := domainmemory.ValidateID("character_id", characterID); err != nil {
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
	conversationID, err := RecentConversationID(queryCtx, tx, characterID)
	if err != nil {
		return ConversationBootstrap{}, err
	}
	if conversationID == "" {
		conversationID = newID()
		now := nowUnixMS()
		if err := InsertConversationWithPromptWindow(queryCtx, tx, conversationID, characterID, now); err != nil {
			return ConversationBootstrap{}, err
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("committing conversation transaction: %w", err)
	}
	return s.loadConversationPostgres(ctx, conversationID)
}

func (s *Store) openOrCreateEndpointConversationPostgres(ctx context.Context, characterID string, binding contracts.Binding, digest string) (ConversationBootstrap, error) {
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

	stored, found, err := SelectEndpointConversation(queryCtx, tx, characterID, binding.Endpoint, digest)
	if err != nil {
		return ConversationBootstrap{}, err
	}
	now := nowUnixMS()
	if !found {
		conversationID := newID()
		if err := InsertConversationWithPromptWindow(queryCtx, tx, conversationID, characterID, now); err != nil {
			return ConversationBootstrap{}, err
		}
		if err := InsertEndpointConversation(
			queryCtx, tx, characterID, binding.Endpoint, digest, conversationID,
			string(binding.Facts.Audience), string(binding.Facts.Initiation), string(binding.Facts.Presentation),
			binding.Facts.PrincipalNamespace, binding.Facts.PrincipalDigest, now,
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
	if err := TouchEndpointConversation(queryCtx, tx, characterID, binding.Endpoint, digest, now); err != nil {
		return ConversationBootstrap{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("committing endpoint conversation transaction: %w", err)
	}
	return s.loadConversationPostgres(ctx, stored.ConversationID)
}

func (s *Store) lookupEndpointForConversationPostgres(ctx context.Context, conversationID string) (contracts.Binding, bool, error) {
	if err := domainmemory.ValidateID("conversation_id", conversationID); err != nil {
		return contracts.Binding{}, false, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return LookupEndpointBinding(queryCtx, s.pool.Raw(), conversationID)
}

func (s *Store) loadConversationPostgres(ctx context.Context, conversationID string) (ConversationBootstrap, error) {
	if err := domainmemory.ValidateID("conversation_id", conversationID); err != nil {
		return ConversationBootstrap{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return LoadConversationBootstrap(queryCtx, s.pool.Raw(), conversationID)
}

func (s *Store) beginTurnPostgres(ctx context.Context, conversationID string, userMessage string) (PersistedTurn, error) {
	if err := domainmemory.ValidateID("conversation_id", conversationID); err != nil {
		return PersistedTurn{}, err
	}
	if err := domainmemory.ValidateContent("user message", userMessage); err != nil {
		return PersistedTurn{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return PersistedTurn{}, fmt.Errorf("beginning user message transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := RequireConversation(queryCtx, tx, conversationID); err != nil {
		return PersistedTurn{}, err
	}
	turnSequence, err := NextSequence(queryCtx, tx, "conversation_turns", conversationID)
	if err != nil {
		return PersistedTurn{}, err
	}
	messageSequence, err := NextSequence(queryCtx, tx, "conversation_messages", conversationID)
	if err != nil {
		return PersistedTurn{}, err
	}
	now := nowUnixMS()
	turnID := newID()
	messageID := newID()
	if err := InsertUserTurn(queryCtx, tx, turnID, conversationID, messageID, userMessage, turnSequence, messageSequence, now); err != nil {
		return PersistedTurn{}, err
	}
	if err := TouchConversation(queryCtx, tx, conversationID, now); err != nil {
		return PersistedTurn{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return PersistedTurn{}, fmt.Errorf("committing user message transaction: %w", err)
	}
	return PersistedTurn{ID: turnID, ConversationID: conversationID, UserMessage: MessageRecord{ID: messageID, ConversationID: conversationID, TurnID: turnID, Sequence: uint64(messageSequence), Role: "user", Content: userMessage, CreatedAtUnixMS: now}}, nil
}

func (s *Store) beginInitiationTurnPostgres(ctx context.Context, conversationID string, evidenceIDs []string) (PersistedTurn, error) {
	if err := domainmemory.ValidateID("conversation_id", conversationID); err != nil {
		return PersistedTurn{}, err
	}
	if len(evidenceIDs) == 0 || len(evidenceIDs) > 8 {
		return PersistedTurn{}, errors.New("initiation evidence count is invalid")
	}
	seenEvidence := make(map[string]struct{}, len(evidenceIDs))
	for _, evidenceID := range evidenceIDs {
		if err := domainmemory.ValidateID("evidence_id", evidenceID); err != nil {
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
	if err := RequireConversation(queryCtx, tx, conversationID); err != nil {
		return PersistedTurn{}, err
	}
	turnSequence, err := NextSequence(queryCtx, tx, "conversation_turns", conversationID)
	if err != nil {
		return PersistedTurn{}, err
	}
	now := nowUnixMS()
	turnID := newID()
	if err := InsertInitiationTurn(queryCtx, tx, turnID, conversationID, turnSequence, now, evidenceIDs); err != nil {
		return PersistedTurn{}, err
	}
	if err := TouchConversation(queryCtx, tx, conversationID, now); err != nil {
		return PersistedTurn{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return PersistedTurn{}, fmt.Errorf("committing initiation turn: %w", err)
	}
	return PersistedTurn{ID: turnID, ConversationID: conversationID}, nil
}

func (s *Store) completeTurnPostgres(ctx context.Context, conversationID string, turnID string, assistantMessage string) (MessageRecord, error) {
	if err := domainmemory.ValidateID("conversation_id", conversationID); err != nil {
		return MessageRecord{}, err
	}
	if err := domainmemory.ValidateID("turn_id", turnID); err != nil {
		return MessageRecord{}, err
	}
	if err := domainmemory.ValidateContent("assistant message", assistantMessage); err != nil {
		return MessageRecord{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return MessageRecord{}, fmt.Errorf("beginning assistant message transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := RequireConversation(queryCtx, tx, conversationID); err != nil {
		return MessageRecord{}, err
	}
	now := nowUnixMS()
	messageSequence, err := NextSequence(queryCtx, tx, "conversation_messages", conversationID)
	if err != nil {
		return MessageRecord{}, err
	}
	messageID := newID()
	if err := CompleteTurn(queryCtx, tx, turnID, conversationID, messageID, assistantMessage, messageSequence, now); err != nil {
		return MessageRecord{}, err
	}
	if err := TouchConversation(queryCtx, tx, conversationID, now); err != nil {
		return MessageRecord{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return MessageRecord{}, fmt.Errorf("committing assistant message transaction: %w", err)
	}
	return MessageRecord{ID: messageID, ConversationID: conversationID, TurnID: turnID, Sequence: uint64(messageSequence), Role: "assistant", Content: assistantMessage, CreatedAtUnixMS: now}, nil
}

func (s *Store) interruptTurnPostgres(ctx context.Context, conversationID string, turnID string, publishedPrefix string) (*MessageRecord, error) {
	if err := domainmemory.ValidateID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	if err := domainmemory.ValidateID("turn_id", turnID); err != nil {
		return nil, err
	}
	if publishedPrefix != "" {
		if err := domainmemory.ValidateContent("published assistant prefix", publishedPrefix); err != nil {
			return nil, err
		}
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("beginning interrupted turn transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := RequireConversation(queryCtx, tx, conversationID); err != nil {
		return nil, err
	}
	now := nowUnixMS()
	if err := InterruptTurn(queryCtx, tx, turnID, conversationID, now); err != nil {
		return nil, err
	}

	var assistant *MessageRecord
	if publishedPrefix != "" {
		messageSequence, err := NextSequence(queryCtx, tx, "conversation_messages", conversationID)
		if err != nil {
			return nil, err
		}
		messageID := newID()
		if err := InsertAssistantMessage(queryCtx, tx, messageID, conversationID, turnID, publishedPrefix, messageSequence, now); err != nil {
			return nil, err
		}
		assistant = &MessageRecord{
			ID:              messageID,
			ConversationID:  conversationID,
			TurnID:          turnID,
			Sequence:        uint64(messageSequence),
			Role:            "assistant",
			Content:         publishedPrefix,
			CreatedAtUnixMS: now,
		}
	}
	if err := TouchConversation(queryCtx, tx, conversationID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("committing interrupted turn transaction: %w", err)
	}
	return assistant, nil
}

func (s *Store) failTurnPostgres(ctx context.Context, conversationID string, turnID string, code string, message string, retryable bool) error {
	if err := domainmemory.ValidateID("conversation_id", conversationID); err != nil {
		return err
	}
	if err := domainmemory.ValidateID("turn_id", turnID); err != nil {
		return err
	}
	if err := domainmemory.ValidateContent("error code", code); err != nil {
		return err
	}
	if err := domainmemory.ValidateContent("error message", message); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	return FailTurn(queryCtx, s.pool.Raw(), turnID, conversationID, code, message, retryable, nowUnixMS())
}

func requireConversationPostgres(ctx context.Context, tx pgx.Tx, conversationID string) error {
	return RequireConversation(ctx, tx, conversationID)
}
