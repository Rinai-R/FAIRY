package memory

import (
	"context"
	"errors"
	"fmt"

	"fairy/interaction"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (s *Store) openOrCreateCharacterConversationPostgres(ctx context.Context, characterID string) (ConversationBootstrap, error) {
	if err := validateID("character_id", characterID); err != nil {
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
	conversationID, err := recentConversationIDPostgres(queryCtx, tx, characterID)
	if err != nil {
		return ConversationBootstrap{}, err
	}
	if conversationID == "" {
		conversationID = newID()
		now := nowUnixMS()
		if _, err := tx.Exec(queryCtx, "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ($1, $2, $3, $3)", conversationID, characterID, now); err != nil {
			return ConversationBootstrap{}, fmt.Errorf("creating conversation: %w", err)
		}
		if _, err := tx.Exec(queryCtx, "INSERT INTO prompt_windows(conversation_id, revision, summary, cutoff_message_sequence, updated_at_ms) VALUES ($1, 1, NULL, 0, $2)", conversationID, now); err != nil {
			return ConversationBootstrap{}, fmt.Errorf("creating prompt window: %w", err)
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("committing conversation transaction: %w", err)
	}
	return s.loadConversationPostgres(ctx, conversationID)
}

func (s *Store) openOrCreateEndpointConversationPostgres(ctx context.Context, characterID string, binding interaction.Binding, digest string) (ConversationBootstrap, error) {
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

	var conversationID, audience, initiation, presentation string
	var namespace, principalDigest pgtype.Text
	err = tx.QueryRow(queryCtx, `
SELECT conversation_id, audience, initiation, presentation, principal_namespace, principal_digest
FROM endpoint_conversations
WHERE character_id = $1 AND endpoint = $2 AND endpoint_key_digest = $3`,
		characterID, binding.Endpoint, digest,
	).Scan(&conversationID, &audience, &initiation, &presentation, &namespace, &principalDigest)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ConversationBootstrap{}, fmt.Errorf("loading endpoint conversation: %w", err)
	}
	now := nowUnixMS()
	if errors.Is(err, pgx.ErrNoRows) {
		conversationID = newID()
		if _, err := tx.Exec(queryCtx, "INSERT INTO conversations(id, character_id, created_at_ms, updated_at_ms) VALUES ($1, $2, $3, $3)", conversationID, characterID, now); err != nil {
			return ConversationBootstrap{}, fmt.Errorf("creating endpoint conversation: %w", err)
		}
		if _, err := tx.Exec(queryCtx, "INSERT INTO prompt_windows(conversation_id, revision, summary, cutoff_message_sequence, updated_at_ms) VALUES ($1, 1, NULL, 0, $2)", conversationID, now); err != nil {
			return ConversationBootstrap{}, fmt.Errorf("creating endpoint prompt window: %w", err)
		}
		if _, err := tx.Exec(queryCtx, `
INSERT INTO endpoint_conversations(
    character_id, endpoint, endpoint_key_digest, conversation_id,
    audience, initiation, presentation, principal_namespace, principal_digest,
    created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
			characterID, binding.Endpoint, digest, conversationID,
			binding.Facts.Audience, binding.Facts.Initiation, binding.Facts.Presentation,
			nullableText(binding.Facts.PrincipalNamespace), nullableText(binding.Facts.PrincipalDigest), now,
		); err != nil {
			return ConversationBootstrap{}, fmt.Errorf("binding endpoint conversation: %w", err)
		}
	} else {
		stored := interaction.Binding{
			Endpoint: binding.Endpoint,
			Facts: interaction.Facts{
				Audience: interaction.AudienceKind(audience), Initiation: interaction.InitiationKind(initiation),
				Presentation:       interaction.PresentationKind(presentation),
				PrincipalNamespace: namespace.String, PrincipalDigest: principalDigest.String,
			},
		}
		if stored != binding {
			return ConversationBootstrap{}, ErrEndpointBindingMismatch
		}
		if _, err := tx.Exec(queryCtx, `
UPDATE endpoint_conversations
SET updated_at_ms = $4
WHERE character_id = $1 AND endpoint = $2 AND endpoint_key_digest = $3`, characterID, binding.Endpoint, digest, now); err != nil {
			return ConversationBootstrap{}, fmt.Errorf("touching endpoint conversation binding: %w", err)
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("committing endpoint conversation transaction: %w", err)
	}
	return s.loadConversationPostgres(ctx, conversationID)
}

func (s *Store) lookupEndpointForConversationPostgres(ctx context.Context, conversationID string) (interaction.Binding, bool, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return interaction.Binding{}, false, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var endpoint, audience, initiation, presentation string
	var namespace, digest pgtype.Text
	err := s.pool.Raw().QueryRow(queryCtx, `
SELECT endpoint, audience, initiation, presentation, principal_namespace, principal_digest
FROM endpoint_conversations
WHERE conversation_id = $1`, conversationID).Scan(&endpoint, &audience, &initiation, &presentation, &namespace, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return interaction.Binding{}, false, nil
	}
	if err != nil {
		return interaction.Binding{}, false, fmt.Errorf("looking up endpoint conversation: %w", err)
	}
	binding := interaction.Binding{
		Endpoint: interaction.EndpointKind(endpoint),
		Facts: interaction.Facts{
			Audience: interaction.AudienceKind(audience), Initiation: interaction.InitiationKind(initiation),
			Presentation:       interaction.PresentationKind(presentation),
			PrincipalNamespace: namespace.String, PrincipalDigest: digest.String,
		},
	}
	if err := binding.Validate(); err != nil {
		return interaction.Binding{}, false, fmt.Errorf("validating stored endpoint conversation: %w", err)
	}
	return binding, true, nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) loadConversationPostgres(ctx context.Context, conversationID string) (ConversationBootstrap, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return ConversationBootstrap{}, err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	var conversation ConversationRecord
	if err := s.pool.Raw().QueryRow(queryCtx, "SELECT id, character_id, created_at_ms, updated_at_ms FROM conversations WHERE id = $1", conversationID).Scan(&conversation.ID, &conversation.CharacterID, &conversation.CreatedAtUnixMS, &conversation.UpdatedAtUnixMS); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("loading conversation: %w", err)
	}
	var prompt PromptWindowRecord
	var summary pgtype.Text
	var promptRevision int64
	var cutoffSequence int64
	if err := s.pool.Raw().QueryRow(queryCtx, "SELECT conversation_id, revision, summary, cutoff_message_sequence, updated_at_ms FROM prompt_windows WHERE conversation_id = $1", conversationID).Scan(&prompt.ConversationID, &promptRevision, &summary, &cutoffSequence, &prompt.UpdatedAtUnixMS); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("loading prompt window: %w", err)
	}
	prompt.Revision = uint64(promptRevision)
	prompt.CutoffMessageSequence = uint64(cutoffSequence)
	if summary.Valid {
		prompt.Summary = &summary.String
	}
	rows, err := s.pool.Raw().Query(queryCtx, "SELECT id, conversation_id, turn_id, sequence, role, content, created_at_ms FROM conversation_messages WHERE conversation_id = $1 ORDER BY sequence ASC", conversationID)
	if err != nil {
		return ConversationBootstrap{}, fmt.Errorf("loading conversation messages: %w", err)
	}
	defer rows.Close()
	messages := make([]MessageRecord, 0)
	for rows.Next() {
		var message MessageRecord
		var sequence int64
		if err := rows.Scan(&message.ID, &message.ConversationID, &message.TurnID, &sequence, &message.Role, &message.Content, &message.CreatedAtUnixMS); err != nil {
			return ConversationBootstrap{}, fmt.Errorf("scanning conversation message: %w", err)
		}
		message.Sequence = uint64(sequence)
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return ConversationBootstrap{}, fmt.Errorf("iterating conversation messages: %w", err)
	}
	return ConversationBootstrap{Conversation: conversation, Messages: messages, PromptWindow: prompt}, nil
}

func (s *Store) beginTurnPostgres(ctx context.Context, conversationID string, userMessage string) (PersistedTurn, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return PersistedTurn{}, err
	}
	if err := validateContent("user message", userMessage); err != nil {
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
	if _, err := tx.Exec(queryCtx, "INSERT INTO conversation_turns(id, conversation_id, sequence, status, extraction_state, created_at_ms, updated_at_ms) VALUES ($1, $2, $3, 'interpreting', 'ineligible', $4, $4)", turnID, conversationID, turnSequence, now); err != nil {
		return PersistedTurn{}, fmt.Errorf("creating turn: %w", err)
	}
	if _, err := tx.Exec(queryCtx, "INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms) VALUES ($1, $2, $3, $4, 'user', $5, $6)", messageID, conversationID, turnID, messageSequence, userMessage, now); err != nil {
		return PersistedTurn{}, fmt.Errorf("writing user message: %w", err)
	}
	if err := touchConversationPostgres(queryCtx, tx, conversationID, now); err != nil {
		return PersistedTurn{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return PersistedTurn{}, fmt.Errorf("committing user message transaction: %w", err)
	}
	return PersistedTurn{ID: turnID, ConversationID: conversationID, UserMessage: MessageRecord{ID: messageID, ConversationID: conversationID, TurnID: turnID, Sequence: uint64(messageSequence), Role: "user", Content: userMessage, CreatedAtUnixMS: now}}, nil
}

func (s *Store) completeTurnPostgres(ctx context.Context, conversationID string, turnID string, assistantMessage string) (MessageRecord, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return MessageRecord{}, err
	}
	if err := validateID("turn_id", turnID); err != nil {
		return MessageRecord{}, err
	}
	if err := validateContent("assistant message", assistantMessage); err != nil {
		return MessageRecord{}, err
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
	changed, err := tx.Exec(queryCtx, "UPDATE conversation_turns SET status = 'completed', extraction_state = 'pending', updated_at_ms = $3 WHERE id = $1 AND conversation_id = $2 AND status IN ('interpreting', 'planning', 'responding')", turnID, conversationID, now)
	if err != nil {
		return MessageRecord{}, fmt.Errorf("updating turn completion: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return MessageRecord{}, errors.New("turn does not belong to conversation or is terminal")
	}
	messageSequence, err := nextSequencePostgres(queryCtx, tx, "conversation_messages", conversationID)
	if err != nil {
		return MessageRecord{}, err
	}
	messageID := newID()
	if _, err := tx.Exec(queryCtx, "INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms) VALUES ($1, $2, $3, $4, 'assistant', $5, $6)", messageID, conversationID, turnID, messageSequence, assistantMessage, now); err != nil {
		return MessageRecord{}, fmt.Errorf("writing assistant message: %w", err)
	}
	if err := touchConversationPostgres(queryCtx, tx, conversationID, now); err != nil {
		return MessageRecord{}, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return MessageRecord{}, fmt.Errorf("committing assistant message transaction: %w", err)
	}
	return MessageRecord{ID: messageID, ConversationID: conversationID, TurnID: turnID, Sequence: uint64(messageSequence), Role: "assistant", Content: assistantMessage, CreatedAtUnixMS: now}, nil
}

func (s *Store) interruptTurnPostgres(ctx context.Context, conversationID string, turnID string, publishedPrefix string) (*MessageRecord, error) {
	if err := validateID("conversation_id", conversationID); err != nil {
		return nil, err
	}
	if err := validateID("turn_id", turnID); err != nil {
		return nil, err
	}
	if publishedPrefix != "" {
		if err := validateContent("published assistant prefix", publishedPrefix); err != nil {
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
	if err := requireConversationPostgres(queryCtx, tx, conversationID); err != nil {
		return nil, err
	}
	now := nowUnixMS()
	changed, err := tx.Exec(queryCtx, `
UPDATE conversation_turns
SET status = 'interrupted',
    extraction_state = 'ineligible',
    error_code = NULL,
    error_message = NULL,
    error_retryable = NULL,
    updated_at_ms = $3
WHERE id = $1
  AND conversation_id = $2
  AND status IN ('interpreting', 'planning', 'responding')`, turnID, conversationID, now)
	if err != nil {
		return nil, fmt.Errorf("updating interrupted turn: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return nil, errors.New("turn does not belong to conversation or is terminal")
	}

	var assistant *MessageRecord
	if publishedPrefix != "" {
		messageSequence, err := nextSequencePostgres(queryCtx, tx, "conversation_messages", conversationID)
		if err != nil {
			return nil, err
		}
		messageID := newID()
		if _, err := tx.Exec(queryCtx, "INSERT INTO conversation_messages(id, conversation_id, turn_id, sequence, role, content, created_at_ms) VALUES ($1, $2, $3, $4, 'assistant', $5, $6)", messageID, conversationID, turnID, messageSequence, publishedPrefix, now); err != nil {
			return nil, fmt.Errorf("writing interrupted assistant prefix: %w", err)
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
	if err := touchConversationPostgres(queryCtx, tx, conversationID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("committing interrupted turn transaction: %w", err)
	}
	return assistant, nil
}

func (s *Store) failTurnPostgres(ctx context.Context, conversationID string, turnID string, code string, message string, retryable bool) error {
	if err := validateID("conversation_id", conversationID); err != nil {
		return err
	}
	if err := validateID("turn_id", turnID); err != nil {
		return err
	}
	if err := validateContent("error code", code); err != nil {
		return err
	}
	if err := validateContent("error message", message); err != nil {
		return err
	}
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	changed, err := s.pool.Raw().Exec(queryCtx, "UPDATE conversation_turns SET status = 'failed', extraction_state = 'ineligible', error_code = $3, error_message = $4, error_retryable = $5, updated_at_ms = $6 WHERE id = $1 AND conversation_id = $2 AND status IN ('interpreting', 'planning', 'responding')", turnID, conversationID, code, message, retryable, nowUnixMS())
	if err != nil {
		return fmt.Errorf("marking turn failed: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("turn does not belong to conversation or is terminal")
	}
	return nil
}

func recentConversationIDPostgres(ctx context.Context, tx pgx.Tx, characterID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
SELECT c.id
FROM conversations c
WHERE c.character_id = $1
  AND NOT EXISTS (
    SELECT 1 FROM endpoint_conversations e WHERE e.conversation_id = c.id
  )
ORDER BY c.updated_at_ms DESC, c.id ASC
LIMIT 1`, characterID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("loading recent conversation: %w", err)
	}
	return id, nil
}

func requireConversationPostgres(ctx context.Context, tx pgx.Tx, conversationID string) error {
	var exists int
	err := tx.QueryRow(ctx, "SELECT 1 FROM conversations WHERE id = $1 FOR UPDATE", conversationID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("conversation does not exist")
	}
	if err != nil {
		return fmt.Errorf("checking conversation: %w", err)
	}
	return nil
}

func nextSequencePostgres(ctx context.Context, tx pgx.Tx, table string, conversationID string) (int64, error) {
	if table != "conversation_turns" && table != "conversation_messages" {
		return 0, fmt.Errorf("reading next sequence from unsupported table %q", table)
	}
	var maxSequence int64
	query := "SELECT COALESCE(MAX(sequence), 0) FROM " + table + " WHERE conversation_id = $1"
	if err := tx.QueryRow(ctx, query, conversationID).Scan(&maxSequence); err != nil {
		return 0, fmt.Errorf("reading next sequence from %s: %w", table, err)
	}
	return maxSequence + 1, nil
}

func touchConversationPostgres(ctx context.Context, tx pgx.Tx, conversationID string, now int64) error {
	if _, err := tx.Exec(ctx, "UPDATE conversations SET updated_at_ms = $2 WHERE id = $1", conversationID, now); err != nil {
		return fmt.Errorf("touching conversation: %w", err)
	}
	return nil
}
