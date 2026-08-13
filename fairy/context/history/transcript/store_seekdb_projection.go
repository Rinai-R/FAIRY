package transcript

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const seekDBConversationActivityQuery = `
SELECT c.id, c.character_id, c.created_at_ms, c.updated_at_ms,
       (SELECT COUNT(*)
        FROM conversation_messages m
        WHERE m.conversation_id = c.id
          AND m.role = 'assistant'
          AND m.created_at_ms >= ?),
       (SELECT COUNT(*)
        FROM conversation_messages m
        WHERE m.conversation_id = c.id
          AND m.role = 'assistant'
          AND m.created_at_ms >= ?),
       (SELECT COUNT(*)
        FROM conversation_messages m
        WHERE m.conversation_id = c.id
          AND m.role = 'user'
          AND m.created_at_ms >= ?),
       (SELECT m.created_at_ms
        FROM conversation_messages m
        WHERE m.conversation_id = c.id
          AND m.role = 'assistant'
        ORDER BY m.created_at_ms DESC, m.sequence DESC
        LIMIT 1)
FROM conversations c
WHERE c.id = ?`

func (s *Store) loadConversationRecordSeekDB(ctx context.Context, conversationID string) (ConversationRecord, error) {
	if err := validateSeekDBIdentifier("conversation_id", conversationID); err != nil {
		return ConversationRecord{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	return loadConversationRecordSeekDB(queryCtx, s.seekDB, conversationID)
}

func loadConversationRecordSeekDB(ctx context.Context, database *sql.DB, conversationID string) (ConversationRecord, error) {
	var conversation ConversationRecord
	if err := database.QueryRowContext(ctx, `
SELECT id, character_id, created_at_ms, updated_at_ms
FROM conversations
WHERE id = ?`, conversationID).Scan(
		&conversation.ID,
		&conversation.CharacterID,
		&conversation.CreatedAtUnixMS,
		&conversation.UpdatedAtUnixMS,
	); err != nil {
		return ConversationRecord{}, fmt.Errorf("loading SeekDB conversation: %w", err)
	}
	if err := validateSeekDBConversationRecord(conversation, conversationID); err != nil {
		return ConversationRecord{}, err
	}
	return conversation, nil
}

func (s *Store) loadConversationActivitySeekDB(ctx context.Context, conversationID string, nowUnixMS int64) (ConversationActivity, error) {
	if err := validateSeekDBIdentifier("conversation_id", conversationID); err != nil {
		return ConversationActivity{}, err
	}
	if nowUnixMS <= 0 {
		return ConversationActivity{}, errors.New("activity evaluation time must be positive")
	}
	thirtyMinuteCutoff := max(int64(0), nowUnixMS-30*time.Minute.Milliseconds())
	fiveMinuteCutoff := max(int64(0), nowUnixMS-5*time.Minute.Milliseconds())
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	var (
		activity                        ConversationActivity
		assistant5, assistant30, user30 int64
		lastAssistant                   sql.NullInt64
	)
	if err := s.seekDB.QueryRowContext(
		queryCtx,
		seekDBConversationActivityQuery,
		fiveMinuteCutoff,
		thirtyMinuteCutoff,
		thirtyMinuteCutoff,
		conversationID,
	).Scan(
		&activity.Conversation.ID,
		&activity.Conversation.CharacterID,
		&activity.Conversation.CreatedAtUnixMS,
		&activity.Conversation.UpdatedAtUnixMS,
		&assistant5,
		&assistant30,
		&user30,
		&lastAssistant,
	); err != nil {
		return ConversationActivity{}, fmt.Errorf("loading SeekDB conversation activity: %w", err)
	}
	if err := validateSeekDBConversationRecord(activity.Conversation, conversationID); err != nil {
		return ConversationActivity{}, err
	}
	if assistant5 < 0 || assistant30 < 0 || user30 < 0 {
		return ConversationActivity{}, errors.New("stored SeekDB conversation activity count is invalid")
	}
	if lastAssistant.Valid && (lastAssistant.Int64 < 0 || lastAssistant.Int64 > nowUnixMS) {
		return ConversationActivity{}, errors.New("assistant message timestamp is after activity evaluation time")
	}
	activity.AssistantMessages5Minutes = uint64(assistant5)
	activity.AssistantMessages30Minutes = uint64(assistant30)
	activity.UserMessages30Minutes = uint64(user30)
	if lastAssistant.Valid {
		value := lastAssistant.Int64
		activity.LastAssistantMessageAtUnixMS = &value
	}
	return activity, nil
}

func (s *Store) loadConversationPromptContextSeekDB(ctx context.Context, conversationID string) (ConversationPromptContext, error) {
	if err := validateSeekDBIdentifier("conversation_id", conversationID); err != nil {
		return ConversationPromptContext{}, err
	}
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	conversation, prompt, err := loadConversationMetadataSeekDB(queryCtx, s.seekDB, conversationID)
	if err != nil {
		return ConversationPromptContext{}, err
	}
	rows, err := s.seekDB.QueryContext(queryCtx, `
SELECT m.id, COALESCE(t.message_id, ''), m.conversation_id, m.turn_id,
       m.sequence, m.role, m.content, m.expression_parts, m.created_at_ms
FROM conversation_messages m
JOIN conversation_turns t
  ON t.id = m.turn_id AND t.conversation_id = m.conversation_id
WHERE m.conversation_id = ? AND m.sequence > ?
ORDER BY m.sequence ASC`, conversationID, int64(prompt.CutoffMessageSequence))
	if err != nil {
		return ConversationPromptContext{}, fmt.Errorf("loading SeekDB conversation prompt messages: %w", err)
	}
	messages, err := scanSeekDBMessages(rows)
	if err != nil {
		return ConversationPromptContext{}, err
	}
	messages = applyPromptProjection(messages, prompt.Projection)
	return ConversationPromptContext{Conversation: conversation, Messages: messages, PromptWindow: prompt}, nil
}

func validateSeekDBConversationRecord(conversation ConversationRecord, expectedID string) error {
	if conversation.ID != expectedID {
		return errors.New("stored SeekDB conversation identity is inconsistent")
	}
	if err := validateSeekDBIdentifier("stored conversation_id", conversation.ID); err != nil {
		return err
	}
	if err := validateSeekDBIdentifier("stored character_id", conversation.CharacterID); err != nil {
		return err
	}
	if conversation.CreatedAtUnixMS < 0 || conversation.UpdatedAtUnixMS < conversation.CreatedAtUnixMS {
		return errors.New("stored SeekDB conversation timestamps are invalid")
	}
	return nil
}
