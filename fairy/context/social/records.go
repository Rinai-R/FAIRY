package social

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
		&entry.FeedbackEvaluationCount, &entry.FeedbackAdoptedCount,
		&entry.FeedbackPositiveCount, &entry.FeedbackPartialCount, &entry.FeedbackNegativeCount,
		&entry.FeedbackScoreBasisPoints, &entry.FeedbackQuarantinedUntilUnixMS,
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
          feedback_evaluation_count, feedback_adopted_count, feedback_positive_count,
          feedback_partial_count, feedback_negative_count, feedback_score_basis_points,
          feedback_quarantined_until_ms,
          created_at_ms, updated_at_ms`,
		id, characterID, conversationID, candidate.Kind, candidate.Situation,
		candidate.Content, candidate.RecallCue, hash, candidate.SourceStartUnixMS, candidate.SourceEndUnixMS, now,
	)
	return ScanSocialMemoryEntry(row)
}

func QuerySocialMemoryContext(ctx context.Context, db Querier, characterID, conversationID string, queryFragments []string, now int64) (SocialMemoryContext, error) {
	rows, err := db.Query(ctx, `
SELECT entry.id, entry.character_id, entry.conversation_id, entry.kind, entry.situation, entry.content, entry.recall_cue, entry.status,
       entry.source_start_ms, entry.source_end_ms, entry.use_count, entry.positive_count, entry.negative_count, entry.unknown_count,
       entry.feedback_evaluation_count, entry.feedback_adopted_count, entry.feedback_positive_count,
       entry.feedback_partial_count, entry.feedback_negative_count, entry.feedback_score_basis_points,
       entry.feedback_quarantined_until_ms,
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
WHERE entry.character_id = $1 AND entry.conversation_id = $2
  AND (entry.status = 'active' OR (entry.status = 'suppressed' AND entry.feedback_quarantined_until_ms <= $4))
  AND ranked.relevance IS NOT NULL
ORDER BY ranked.relevance DESC,
         CASE WHEN entry.status = 'active' THEN 0 ELSE 1 END ASC,
         entry.feedback_score_basis_points DESC,
         updated_at_ms DESC,
         id ASC
LIMIT 12`, characterID, conversationID, queryFragments, now)
	if err != nil {
		return SocialMemoryContext{}, fmt.Errorf("querying social memory: %w", err)
	}
	defer rows.Close()
	return collectSocialMemoryContext(rows, 9, 1800)
}

func QueryCharacterSocialMemoryContext(ctx context.Context, db Querier, characterID string, queryFragments []string, now int64) (SocialMemoryContext, error) {
	rows, err := db.Query(ctx, `
SELECT entry.id, entry.character_id, entry.conversation_id, entry.kind, entry.situation, entry.content, entry.recall_cue, entry.status,
       entry.source_start_ms, entry.source_end_ms, entry.use_count, entry.positive_count, entry.negative_count, entry.unknown_count,
       entry.feedback_evaluation_count, entry.feedback_adopted_count, entry.feedback_positive_count,
       entry.feedback_partial_count, entry.feedback_negative_count, entry.feedback_score_basis_points,
       entry.feedback_quarantined_until_ms,
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
WHERE entry.character_id = $1
  AND (entry.status = 'active' OR (entry.status = 'suppressed' AND entry.feedback_quarantined_until_ms <= $3))
  AND ranked.relevance IS NOT NULL
ORDER BY ranked.relevance DESC,
         CASE WHEN entry.status = 'active' THEN 0 ELSE 1 END ASC,
         entry.feedback_score_basis_points DESC,
         updated_at_ms DESC,
         id ASC
LIMIT 24`, characterID, queryFragments, now)
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

type socialFeedbackEntryState struct {
	status           string
	positiveCount    int64
	partialCount     int64
	negativeCount    int64
	quarantinedUntil *int64
}

func RecordSocialFeedbackBatch(
	ctx context.Context,
	tx pgx.Tx,
	input SocialFeedbackBatchInput,
	now int64,
) (SocialFeedbackBatchResult, error) {
	var turnCount int
	if err := tx.QueryRow(ctx, `
SELECT COUNT(*) FROM conversation_turns
WHERE id = $1 AND conversation_id = $2 AND status = 'completed'`, input.TurnID, input.ConversationID).Scan(&turnCount); err != nil {
		return SocialFeedbackBatchResult{}, fmt.Errorf("checking social feedback turn: %w", err)
	}
	if turnCount != 1 {
		return SocialFeedbackBatchResult{}, errors.New("social feedback turn does not belong to the completed conversation")
	}
	entryIDs := make([]string, 0, len(input.Evaluations))
	for _, evaluation := range input.Evaluations {
		entryIDs = append(entryIDs, evaluation.EntryID)
	}
	rows, err := tx.Query(ctx, `
SELECT id, status, feedback_positive_count, feedback_partial_count,
       feedback_negative_count, feedback_quarantined_until_ms
FROM social_memory_entries
WHERE character_id = $1 AND conversation_id = $2 AND id = ANY($3)
FOR UPDATE`, input.CharacterID, input.ConversationID, entryIDs)
	if err != nil {
		return SocialFeedbackBatchResult{}, fmt.Errorf("locking social feedback entries: %w", err)
	}
	states := make(map[string]socialFeedbackEntryState, len(entryIDs))
	for rows.Next() {
		var id string
		var state socialFeedbackEntryState
		if err := rows.Scan(&id, &state.status, &state.positiveCount, &state.partialCount, &state.negativeCount, &state.quarantinedUntil); err != nil {
			rows.Close()
			return SocialFeedbackBatchResult{}, fmt.Errorf("scanning social feedback entry state: %w", err)
		}
		states[id] = state
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return SocialFeedbackBatchResult{}, fmt.Errorf("iterating social feedback entry states: %w", err)
	}
	rows.Close()
	if len(states) != len(entryIDs) {
		return SocialFeedbackBatchResult{}, errors.New("social feedback entries do not belong to the conversation")
	}

	result := SocialFeedbackBatchResult{Events: make([]SocialFeedbackEvent, 0, len(input.Evaluations)), NoChange: true}
	for _, evaluation := range input.Evaluations {
		evaluation.EvidenceMessageIDs = append([]string{}, evaluation.EvidenceMessageIDs...)
		slices.Sort(evaluation.EvidenceMessageIDs)
		event, exists, err := loadSocialFeedbackEvent(ctx, tx, input.TurnID, evaluation.EntryID)
		if err != nil {
			return SocialFeedbackBatchResult{}, err
		}
		if exists {
			if !sameSocialFeedbackEvent(event, input, evaluation) {
				return SocialFeedbackBatchResult{}, errors.New("social feedback event conflicts with an existing turn and entry")
			}
			result.Events = append(result.Events, event)
			continue
		}
		event, err = insertSocialFeedbackEvent(ctx, tx, input, evaluation, now)
		if err != nil {
			return SocialFeedbackBatchResult{}, err
		}
		state := states[evaluation.EntryID]
		if err := applySocialFeedbackAggregate(ctx, tx, evaluation, state, now); err != nil {
			return SocialFeedbackBatchResult{}, err
		}
		result.NoChange = false
		result.Events = append(result.Events, event)
	}
	return result, nil
}

func loadSocialFeedbackEvent(ctx context.Context, tx pgx.Tx, turnID, entryID string) (SocialFeedbackEvent, bool, error) {
	event, err := scanSocialFeedbackEvent(tx.QueryRow(ctx, `
SELECT id, character_id, conversation_id, turn_id, entry_id, adoption, outcome, credit,
       evidence_message_ids, observed_message_count, evaluator_revision, created_at_ms
FROM social_memory_feedback_events WHERE turn_id = $1 AND entry_id = $2`, turnID, entryID))
	if errors.Is(err, pgx.ErrNoRows) {
		return SocialFeedbackEvent{}, false, nil
	}
	if err != nil {
		return SocialFeedbackEvent{}, false, fmt.Errorf("loading social feedback event: %w", err)
	}
	return event, true, nil
}

func insertSocialFeedbackEvent(
	ctx context.Context,
	tx pgx.Tx,
	input SocialFeedbackBatchInput,
	evaluation SocialFeedbackEvaluation,
	now int64,
) (SocialFeedbackEvent, error) {
	evidenceJSON, err := json.Marshal(evaluation.EvidenceMessageIDs)
	if err != nil {
		return SocialFeedbackEvent{}, fmt.Errorf("encoding social feedback evidence IDs: %w", err)
	}
	event, err := scanSocialFeedbackEvent(tx.QueryRow(ctx, `
INSERT INTO social_memory_feedback_events(
  id, character_id, conversation_id, turn_id, entry_id, adoption, outcome, credit,
  evidence_message_ids, observed_message_count, evaluator_revision, created_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id, character_id, conversation_id, turn_id, entry_id, adoption, outcome, credit,
          evidence_message_ids, observed_message_count, evaluator_revision, created_at_ms`,
		newID(), input.CharacterID, input.ConversationID, input.TurnID, evaluation.EntryID,
		evaluation.Adoption, evaluation.Outcome, evaluation.Credit, evidenceJSON,
		input.ObservedMessageCount, input.EvaluatorRevision, now,
	))
	if err != nil {
		return SocialFeedbackEvent{}, fmt.Errorf("inserting social feedback event: %w", err)
	}
	return event, nil
}

func scanSocialFeedbackEvent(row scanner) (SocialFeedbackEvent, error) {
	var event SocialFeedbackEvent
	var evidenceJSON []byte
	if err := row.Scan(
		&event.ID, &event.CharacterID, &event.ConversationID, &event.TurnID, &event.EntryID,
		&event.Adoption, &event.Outcome, &event.Credit, &evidenceJSON,
		&event.ObservedMessageCount, &event.EvaluatorRevision, &event.CreatedAtUnixMS,
	); err != nil {
		return SocialFeedbackEvent{}, err
	}
	if err := json.Unmarshal(evidenceJSON, &event.EvidenceMessageIDs); err != nil {
		return SocialFeedbackEvent{}, fmt.Errorf("decoding stored social feedback evidence IDs: %w", err)
	}
	return event, nil
}

func sameSocialFeedbackEvent(event SocialFeedbackEvent, input SocialFeedbackBatchInput, evaluation SocialFeedbackEvaluation) bool {
	return event.CharacterID == input.CharacterID &&
		event.ConversationID == input.ConversationID &&
		event.TurnID == input.TurnID &&
		event.EntryID == evaluation.EntryID &&
		event.Adoption == evaluation.Adoption &&
		event.Outcome == evaluation.Outcome &&
		event.Credit == evaluation.Credit &&
		slices.Equal(event.EvidenceMessageIDs, evaluation.EvidenceMessageIDs) &&
		event.ObservedMessageCount == input.ObservedMessageCount &&
		event.EvaluatorRevision == input.EvaluatorRevision
}

func applySocialFeedbackAggregate(
	ctx context.Context,
	tx pgx.Tx,
	evaluation SocialFeedbackEvaluation,
	state socialFeedbackEntryState,
	now int64,
) error {
	adopted, positive, partial, negative := int64(0), int64(0), int64(0), int64(0)
	if evaluation.Adoption == SocialFeedbackAdopted {
		adopted = 1
	}
	if evaluation.Adoption == SocialFeedbackAdopted && evaluation.Credit == SocialFeedbackCreditEntry {
		switch evaluation.Outcome {
		case SocialFeedbackPositive:
			positive = 1
		case SocialFeedbackPartial:
			partial = 1
		case SocialFeedbackNegative:
			negative = 1
		}
	}
	nextPositive := state.positiveCount + positive
	nextPartial := state.partialCount + partial
	nextNegative := state.negativeCount + negative
	score := SocialFeedbackScoreBasisPoints(nextPositive, nextPartial, nextNegative)
	status := state.status
	quarantinedUntil := state.quarantinedUntil
	if positive+partial > 0 && status == "suppressed" {
		status = "active"
		quarantinedUntil = nil
	} else if negative > 0 && nextNegative >= SocialNegativeSuppressThreshold && score <= SocialFeedbackQuarantineScore {
		until := now + SocialFeedbackQuarantineMS
		status = "suppressed"
		quarantinedUntil = &until
	}
	changed, err := tx.Exec(ctx, `
UPDATE social_memory_entries
SET feedback_evaluation_count = feedback_evaluation_count + 1,
    feedback_adopted_count = feedback_adopted_count + $2,
    feedback_positive_count = $3,
    feedback_partial_count = $4,
    feedback_negative_count = $5,
    feedback_score_basis_points = $6,
    status = $7,
    feedback_quarantined_until_ms = $8,
    updated_at_ms = $9
WHERE id = $1`, evaluation.EntryID, adopted, nextPositive, nextPartial, nextNegative,
		score, status, quarantinedUntil, now)
	if err != nil {
		return fmt.Errorf("updating social feedback aggregate: %w", err)
	}
	if changed.RowsAffected() != 1 {
		return errors.New("social feedback aggregate did not update exactly one entry")
	}
	return nil
}
