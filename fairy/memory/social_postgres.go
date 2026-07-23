package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *Store) storeSocialMemoryEntriesPostgres(ctx context.Context, input SocialMemoryBatchInput) ([]SocialMemoryEntry, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("beginning social memory transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := verifySocialConversationScope(queryCtx, tx, input.CharacterID, input.ConversationID); err != nil {
		return nil, err
	}
	now := nowUnixMS()
	entries := make([]SocialMemoryEntry, 0, len(input.Entries))
	for _, candidate := range input.Entries {
		id := newID()
		hash := socialMemoryContentHash(candidate)
		row := tx.QueryRow(queryCtx, `
INSERT INTO social_memory_entries(
    id, character_id, conversation_id, kind, situation, content, recall_cue,
    content_hash, status, source_start_ms, source_end_ms,
    use_count, positive_count, negative_count, unknown_count, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'active', $9, $10, 0, 0, 0, 0, $11, $11)
ON CONFLICT (conversation_id, kind, content_hash) DO UPDATE
SET updated_at_ms = EXCLUDED.updated_at_ms,
    source_start_ms = LEAST(social_memory_entries.source_start_ms, EXCLUDED.source_start_ms),
    source_end_ms = GREATEST(social_memory_entries.source_end_ms, EXCLUDED.source_end_ms)
RETURNING id, character_id, conversation_id, kind, situation, content, recall_cue, status,
          source_start_ms, source_end_ms, use_count, positive_count, negative_count, unknown_count,
          created_at_ms, updated_at_ms`,
			id, input.CharacterID, input.ConversationID, candidate.Kind, candidate.Situation,
			candidate.Content, candidate.RecallCue, hash, candidate.SourceStartUnixMS, candidate.SourceEndUnixMS, now,
		)
		entry, err := scanSocialMemoryEntry(row)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("committing social memory transaction: %w", err)
	}
	return entries, nil
}

func (s *Store) retrieveSocialMemoryContextPostgres(ctx context.Context, characterID, conversationID, query string) (SocialMemoryContext, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	if err := verifySocialConversationScope(queryCtx, s.pool.Raw(), characterID, conversationID); err != nil {
		return SocialMemoryContext{}, err
	}
	rows, err := s.pool.Raw().Query(queryCtx, `
SELECT id, character_id, conversation_id, kind, situation, content, recall_cue, status,
       source_start_ms, source_end_ms, use_count, positive_count, negative_count, unknown_count,
       created_at_ms, updated_at_ms
FROM social_memory_entries
WHERE character_id = $1 AND conversation_id = $2 AND status = 'active'
  AND (
    situation ILIKE '%' || $3 || '%' OR content ILIKE '%' || $3 || '%' OR recall_cue ILIKE '%' || $3 || '%'
    OR situation OPERATOR(public.%) $3 OR content OPERATOR(public.%) $3 OR recall_cue OPERATOR(public.%) $3
    OR $3 OPERATOR(public.<%) situation OR $3 OPERATOR(public.<%) content OR $3 OPERATOR(public.<%) recall_cue
  )
ORDER BY GREATEST(
           public.similarity(situation, $3), public.similarity(content, $3), public.similarity(recall_cue, $3),
           public.word_similarity($3, situation), public.word_similarity($3, content), public.word_similarity($3, recall_cue)
         ) DESC,
         (positive_count - negative_count) DESC,
         updated_at_ms DESC,
         id ASC
LIMIT 12`, characterID, conversationID, query)
	if err != nil {
		return SocialMemoryContext{}, fmt.Errorf("querying social memory: %w", err)
	}
	defer rows.Close()
	entries := make([]SocialMemoryEntry, 0, 9)
	perKind := make(map[string]int, 3)
	remaining := 1800
	for rows.Next() {
		entry, scanErr := scanSocialMemoryEntry(rows)
		if scanErr != nil {
			return SocialMemoryContext{}, scanErr
		}
		if perKind[entry.Kind] >= 3 {
			continue
		}
		length := len([]rune(entry.Situation)) + len([]rune(entry.Content)) + len([]rune(entry.RecallCue))
		if length > remaining {
			continue
		}
		remaining -= length
		perKind[entry.Kind]++
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return SocialMemoryContext{}, fmt.Errorf("iterating social memory: %w", err)
	}
	return SocialMemoryContext{Entries: entries}, nil
}

func (s *Store) recordSocialReplyFeedbackPostgres(ctx context.Context, input SocialReplyFeedbackInput) (SocialReplyFeedback, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	tx, err := s.pool.Raw().Begin(queryCtx)
	if err != nil {
		return SocialReplyFeedback{}, fmt.Errorf("beginning social feedback transaction: %w", err)
	}
	defer tx.Rollback(queryCtx)
	if err := verifySocialConversationScope(queryCtx, tx, input.CharacterID, input.ConversationID); err != nil {
		return SocialReplyFeedback{}, err
	}
	var turnCount int
	if err := tx.QueryRow(queryCtx, "SELECT COUNT(*) FROM conversation_turns WHERE id = $1 AND conversation_id = $2", input.TurnID, input.ConversationID).Scan(&turnCount); err != nil {
		return SocialReplyFeedback{}, fmt.Errorf("checking social feedback turn: %w", err)
	}
	if turnCount != 1 {
		return SocialReplyFeedback{}, errors.New("social feedback turn does not belong to the conversation")
	}
	if len(input.EntryIDs) > 0 {
		var entryCount int
		if err := tx.QueryRow(queryCtx, "SELECT COUNT(*) FROM social_memory_entries WHERE character_id = $1 AND conversation_id = $2 AND id = ANY($3)", input.CharacterID, input.ConversationID, input.EntryIDs).Scan(&entryCount); err != nil {
			return SocialReplyFeedback{}, fmt.Errorf("checking social feedback entries: %w", err)
		}
		if entryCount != len(input.EntryIDs) {
			return SocialReplyFeedback{}, errors.New("social feedback entries do not belong to the conversation")
		}
	}
	entryIDsJSON, err := json.Marshal(input.EntryIDs)
	if err != nil {
		return SocialReplyFeedback{}, fmt.Errorf("serializing social feedback entry IDs: %w", err)
	}
	id := newID()
	now := nowUnixMS()
	if _, err := tx.Exec(queryCtx, `
INSERT INTO social_reply_feedback(
    id, character_id, conversation_id, turn_id, outcome, entry_ids_json,
    observed_message_count, created_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, input.CharacterID, input.ConversationID, input.TurnID, input.Outcome,
		entryIDsJSON, input.ObservedMessageCount, now,
	); err != nil {
		return SocialReplyFeedback{}, fmt.Errorf("inserting social feedback: %w", err)
	}
	if len(input.EntryIDs) > 0 {
		positive, negative, unknown := 0, 0, 0
		switch input.Outcome {
		case SocialFeedbackPositive:
			positive = 1
		case SocialFeedbackNegative:
			negative = 1
		case SocialFeedbackUnknown:
			unknown = 1
		}
		changed, err := tx.Exec(queryCtx, `
UPDATE social_memory_entries
SET use_count = use_count + 1,
    positive_count = positive_count + $4,
    negative_count = negative_count + $5,
    unknown_count = unknown_count + $6,
    updated_at_ms = $7
WHERE character_id = $1 AND conversation_id = $2 AND id = ANY($3)`,
			input.CharacterID, input.ConversationID, input.EntryIDs, positive, negative, unknown, now,
		)
		if err != nil {
			return SocialReplyFeedback{}, fmt.Errorf("updating social memory feedback counters: %w", err)
		}
		if changed.RowsAffected() != int64(len(input.EntryIDs)) {
			return SocialReplyFeedback{}, errors.New("social feedback did not update every referenced entry")
		}
		if _, err := tx.Exec(queryCtx, `
UPDATE social_memory_entries
SET status = 'suppressed', updated_at_ms = $4
WHERE character_id = $1 AND conversation_id = $2 AND id = ANY($3)
  AND status = 'active'
  AND negative_count >= $5
  AND negative_count >= positive_count + 2`,
			input.CharacterID, input.ConversationID, input.EntryIDs, now, SocialNegativeSuppressThreshold,
		); err != nil {
			return SocialReplyFeedback{}, fmt.Errorf("suppressing social memory entries: %w", err)
		}
	}
	if err := tx.Commit(queryCtx); err != nil {
		return SocialReplyFeedback{}, fmt.Errorf("committing social feedback transaction: %w", err)
	}
	return SocialReplyFeedback{
		ID: id, CharacterID: input.CharacterID, ConversationID: input.ConversationID,
		TurnID: input.TurnID, EntryIDs: append([]string(nil), input.EntryIDs...), Outcome: input.Outcome,
		ObservedMessageCount: input.ObservedMessageCount, CreatedAtUnixMS: now,
	}, nil
}

type socialScopeQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func verifySocialConversationScope(ctx context.Context, db socialScopeQuerier, characterID, conversationID string) error {
	var storedCharacterID string
	if err := db.QueryRow(ctx, "SELECT character_id FROM conversations WHERE id = $1", conversationID).Scan(&storedCharacterID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("social memory conversation does not exist")
		}
		return fmt.Errorf("checking social memory conversation: %w", err)
	}
	if storedCharacterID != characterID {
		return errors.New("social memory character does not own the conversation")
	}
	return nil
}

type socialMemoryScanner interface {
	Scan(...any) error
}

func scanSocialMemoryEntry(scanner socialMemoryScanner) (SocialMemoryEntry, error) {
	var entry SocialMemoryEntry
	if err := scanner.Scan(
		&entry.ID, &entry.CharacterID, &entry.ConversationID, &entry.Kind,
		&entry.Situation, &entry.Content, &entry.RecallCue, &entry.Status,
		&entry.SourceStartUnixMS, &entry.SourceEndUnixMS, &entry.UseCount,
		&entry.PositiveCount, &entry.NegativeCount, &entry.UnknownCount,
		&entry.CreatedAtUnixMS, &entry.UpdatedAtUnixMS,
	); err != nil {
		return SocialMemoryEntry{}, fmt.Errorf("scanning social memory entry: %w", err)
	}
	if !validSocialMemoryKind(entry.Kind) || (entry.Status != "active" && entry.Status != "suppressed") {
		return SocialMemoryEntry{}, errors.New("stored social memory entry is invalid")
	}
	return entry, nil
}

func socialMemoryContentHash(entry SocialMemoryEntryInput) string {
	normalize := func(value string) string { return strings.Join(strings.Fields(value), " ") }
	payload := strings.Join([]string{entry.Kind, normalize(entry.Situation), normalize(entry.Content), normalize(entry.RecallCue)}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}
