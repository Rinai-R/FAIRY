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

type RowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func VerifySocialConversationScope(ctx context.Context, db RowQuerier, characterID, conversationID string) error {
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

func ScanSocialMemoryEntry(row scanner) (SocialMemoryEntry, error) {
	var entry SocialMemoryEntry
	if err := row.Scan(
		&entry.ID, &entry.CharacterID, &entry.ConversationID, &entry.Kind,
		&entry.Situation, &entry.Content, &entry.RecallCue, &entry.Status,
		&entry.SourceStartUnixMS, &entry.SourceEndUnixMS, &entry.UseCount,
		&entry.PositiveCount, &entry.NegativeCount, &entry.UnknownCount,
		&entry.CreatedAtUnixMS, &entry.UpdatedAtUnixMS,
	); err != nil {
		return SocialMemoryEntry{}, fmt.Errorf("scanning social memory entry: %w", err)
	}
	if !ValidSocialMemoryKind(entry.Kind) || (entry.Status != "active" && entry.Status != "suppressed") {
		return SocialMemoryEntry{}, errors.New("stored social memory entry is invalid")
	}
	return entry, nil
}

func SocialMemoryContentHash(entry SocialMemoryEntryInput) string {
	normalize := func(value string) string { return strings.Join(strings.Fields(value), " ") }
	payload := strings.Join([]string{entry.Kind, normalize(entry.Situation), normalize(entry.Content), normalize(entry.RecallCue)}, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

func InsertSocialMemoryEntry(
	ctx context.Context,
	tx pgx.Tx,
	id, characterID, conversationID string,
	candidate SocialMemoryEntryInput,
	now int64,
) (SocialMemoryEntry, error) {
	hash := SocialMemoryContentHash(candidate)
	row := tx.QueryRow(ctx, `
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
		id, characterID, conversationID, candidate.Kind, candidate.Situation,
		candidate.Content, candidate.RecallCue, hash, candidate.SourceStartUnixMS, candidate.SourceEndUnixMS, now,
	)
	return ScanSocialMemoryEntry(row)
}

func QuerySocialMemoryContext(ctx context.Context, db Querier, characterID, conversationID string, queryFragments []string) (SocialMemoryContext, error) {
	rows, err := db.Query(ctx, `
SELECT entry.id, entry.character_id, entry.conversation_id, entry.kind, entry.situation, entry.content, entry.recall_cue, entry.status,
       entry.source_start_ms, entry.source_end_ms, entry.use_count, entry.positive_count, entry.negative_count, entry.unknown_count,
       entry.created_at_ms, entry.updated_at_ms
FROM social_memory_entries AS entry
CROSS JOIN LATERAL (
  SELECT MAX(GREATEST(
           public.similarity(entry.situation, fragment.value), public.similarity(entry.content, fragment.value), public.similarity(entry.recall_cue, fragment.value),
           public.word_similarity(fragment.value, entry.situation), public.word_similarity(fragment.value, entry.content), public.word_similarity(fragment.value, entry.recall_cue)
         )) AS relevance
  FROM unnest($3::text[]) AS fragment(value)
  WHERE entry.situation ILIKE '%' || fragment.value || '%' OR entry.content ILIKE '%' || fragment.value || '%' OR entry.recall_cue ILIKE '%' || fragment.value || '%'
     OR entry.situation OPERATOR(public.%) fragment.value OR entry.content OPERATOR(public.%) fragment.value OR entry.recall_cue OPERATOR(public.%) fragment.value
     OR fragment.value OPERATOR(public.<%) entry.situation OR fragment.value OPERATOR(public.<%) entry.content OR fragment.value OPERATOR(public.<%) entry.recall_cue
) AS ranked
WHERE entry.character_id = $1 AND entry.conversation_id = $2 AND entry.status = 'active' AND ranked.relevance IS NOT NULL
ORDER BY ranked.relevance DESC,
         (positive_count - negative_count) DESC,
         updated_at_ms DESC,
         id ASC
LIMIT 12`, characterID, conversationID, queryFragments)
	if err != nil {
		return SocialMemoryContext{}, fmt.Errorf("querying social memory: %w", err)
	}
	defer rows.Close()
	return collectSocialMemoryContext(rows, 9, 1800)
}

func QueryCharacterSocialMemoryContext(ctx context.Context, db Querier, characterID string, queryFragments []string) (SocialMemoryContext, error) {
	rows, err := db.Query(ctx, `
SELECT entry.id, entry.character_id, entry.conversation_id, entry.kind, entry.situation, entry.content, entry.recall_cue, entry.status,
       entry.source_start_ms, entry.source_end_ms, entry.use_count, entry.positive_count, entry.negative_count, entry.unknown_count,
       entry.created_at_ms, entry.updated_at_ms
FROM social_memory_entries AS entry
CROSS JOIN LATERAL (
  SELECT MAX(GREATEST(
           public.similarity(entry.situation, fragment.value), public.similarity(entry.content, fragment.value), public.similarity(entry.recall_cue, fragment.value),
           public.word_similarity(fragment.value, entry.situation), public.word_similarity(fragment.value, entry.content), public.word_similarity(fragment.value, entry.recall_cue)
         )) AS relevance
  FROM unnest($2::text[]) AS fragment(value)
  WHERE entry.situation ILIKE '%' || fragment.value || '%' OR entry.content ILIKE '%' || fragment.value || '%' OR entry.recall_cue ILIKE '%' || fragment.value || '%'
     OR entry.situation OPERATOR(public.%) fragment.value OR entry.content OPERATOR(public.%) fragment.value OR entry.recall_cue OPERATOR(public.%) fragment.value
     OR fragment.value OPERATOR(public.<%) entry.situation OR fragment.value OPERATOR(public.<%) entry.content OR fragment.value OPERATOR(public.<%) entry.recall_cue
) AS ranked
WHERE entry.character_id = $1 AND entry.status = 'active' AND ranked.relevance IS NOT NULL
ORDER BY ranked.relevance DESC,
         (positive_count - negative_count) DESC,
         updated_at_ms DESC,
         id ASC
LIMIT 24`, characterID, queryFragments)
	if err != nil {
		return SocialMemoryContext{}, fmt.Errorf("querying character social memory: %w", err)
	}
	defer rows.Close()
	return collectSocialMemoryContext(rows, 12, 1800)
}

func collectSocialMemoryContext(rows pgx.Rows, capacity, runeBudget int) (SocialMemoryContext, error) {
	entries := make([]SocialMemoryEntry, 0, capacity)
	perKind := make(map[string]int, 3)
	remaining := runeBudget
	for rows.Next() {
		entry, scanErr := ScanSocialMemoryEntry(rows)
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

func RecordSocialReplyFeedback(
	ctx context.Context,
	tx pgx.Tx,
	input SocialReplyFeedbackInput,
	id string,
	now int64,
	negativeSuppressThreshold int,
) (SocialReplyFeedback, error) {
	var turnCount int
	if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM conversation_turns WHERE id = $1 AND conversation_id = $2", input.TurnID, input.ConversationID).Scan(&turnCount); err != nil {
		return SocialReplyFeedback{}, fmt.Errorf("checking social feedback turn: %w", err)
	}
	if turnCount != 1 {
		return SocialReplyFeedback{}, errors.New("social feedback turn does not belong to the conversation")
	}
	if len(input.EntryIDs) > 0 {
		var entryCount int
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM social_memory_entries WHERE character_id = $1 AND conversation_id = $2 AND id = ANY($3)", input.CharacterID, input.ConversationID, input.EntryIDs).Scan(&entryCount); err != nil {
			return SocialReplyFeedback{}, fmt.Errorf("checking social feedback entries: %w", err)
		}
		if entryCount != len(input.EntryIDs) {
			return SocialReplyFeedback{}, errors.New("social feedback entries do not belong to the conversation")
		}
	}
	entryIDs := input.EntryIDs
	if entryIDs == nil {
		entryIDs = []string{}
	}
	entryIDsJSON, err := json.Marshal(entryIDs)
	if err != nil {
		return SocialReplyFeedback{}, fmt.Errorf("serializing social feedback entry IDs: %w", err)
	}
	if _, err := tx.Exec(ctx, `
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
		changed, err := tx.Exec(ctx, `
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
		if _, err := tx.Exec(ctx, `
UPDATE social_memory_entries
SET status = 'suppressed', updated_at_ms = $4
WHERE character_id = $1 AND conversation_id = $2 AND id = ANY($3)
  AND status = 'active'
  AND negative_count >= $5
  AND negative_count >= positive_count + 2`,
			input.CharacterID, input.ConversationID, input.EntryIDs, now, negativeSuppressThreshold,
		); err != nil {
			return SocialReplyFeedback{}, fmt.Errorf("suppressing social memory entries: %w", err)
		}
	}
	return SocialReplyFeedback{
		ID: id, CharacterID: input.CharacterID, ConversationID: input.ConversationID,
		TurnID: input.TurnID, EntryIDs: append([]string{}, entryIDs...), Outcome: input.Outcome,
		ObservedMessageCount: input.ObservedMessageCount, CreatedAtUnixMS: now,
	}, nil
}

func QueryRecentSocialFeedbackSummary(ctx context.Context, db Querier, characterID, conversationID string, limit int) (RecentSocialFeedbackSummary, error) {
	rows, err := db.Query(ctx, `
SELECT outcome, observed_message_count
FROM social_reply_feedback
WHERE character_id = $1 AND conversation_id = $2
ORDER BY created_at_ms DESC, id ASC
LIMIT $3`, characterID, conversationID, limit)
	if err != nil {
		return RecentSocialFeedbackSummary{}, fmt.Errorf("querying recent social feedback: %w", err)
	}
	defer rows.Close()
	var summary RecentSocialFeedbackSummary
	for rows.Next() {
		var outcome string
		var observed int
		if err := rows.Scan(&outcome, &observed); err != nil {
			return RecentSocialFeedbackSummary{}, fmt.Errorf("scanning recent social feedback: %w", err)
		}
		if summary.SampleCount == 0 {
			summary.LatestOutcome = outcome
		}
		switch outcome {
		case SocialFeedbackPositive:
			summary.PositiveCount++
		case SocialFeedbackNegative:
			summary.NegativeCount++
		case SocialFeedbackUnknown:
			summary.UnknownCount++
		default:
			return RecentSocialFeedbackSummary{}, errors.New("stored social feedback outcome is invalid")
		}
		if observed < 0 {
			return RecentSocialFeedbackSummary{}, errors.New("stored social feedback observation count is invalid")
		}
		summary.SampleCount++
		summary.ObservedMessageCount += observed
	}
	if err := rows.Err(); err != nil {
		return RecentSocialFeedbackSummary{}, fmt.Errorf("iterating recent social feedback: %w", err)
	}
	return summary, nil
}
