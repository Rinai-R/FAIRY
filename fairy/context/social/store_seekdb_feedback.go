package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

func (s *Store) recordSocialFeedbackBatchSeekDB(ctx context.Context, input SocialFeedbackBatchInput) (SocialFeedbackBatchResult, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return SocialFeedbackBatchResult{}, fmt.Errorf("beginning SeekDB social feedback batch transaction: %w", err)
	}
	defer tx.Rollback()
	if err := lockSeekDBSocialConversation(queryCtx, tx, input.CharacterID, input.ConversationID); err != nil {
		return SocialFeedbackBatchResult{}, err
	}
	if err := lockSeekDBSocialFeedbackTurn(queryCtx, tx, input.ConversationID, input.TurnID); err != nil {
		return SocialFeedbackBatchResult{}, err
	}
	states, err := lockSeekDBSocialFeedbackEntries(queryCtx, tx, input.CharacterID, input.ConversationID, input.Evaluations)
	if err != nil {
		return SocialFeedbackBatchResult{}, err
	}
	result, err := recordSeekDBSocialFeedbackBatch(queryCtx, tx, input, states, s.currentUnixMS())
	if err != nil {
		return SocialFeedbackBatchResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SocialFeedbackBatchResult{}, fmt.Errorf("committing SeekDB social feedback batch transaction: %w", err)
	}
	return result, nil
}

func lockSeekDBSocialFeedbackTurn(ctx context.Context, tx seekDBTx, conversationID, turnID string) error {
	var storedID string
	err := tx.QueryRowContext(ctx, `
SELECT id FROM conversation_turns
WHERE id = ? AND conversation_id = ? AND status = 'completed'
FOR UPDATE`, turnID, conversationID).Scan(&storedID)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("social feedback turn does not belong to the completed conversation")
	}
	if err != nil {
		return fmt.Errorf("locking social feedback turn: %w", err)
	}
	return nil
}

func lockSeekDBSocialFeedbackEntries(
	ctx context.Context,
	tx seekDBTx,
	characterID, conversationID string,
	evaluations []SocialFeedbackEvaluation,
) (map[string]socialFeedbackEntryState, error) {
	entryIDs := make([]string, 0, len(evaluations))
	seen := make(map[string]struct{}, len(evaluations))
	for _, evaluation := range evaluations {
		if _, exists := seen[evaluation.EntryID]; exists {
			continue
		}
		seen[evaluation.EntryID] = struct{}{}
		entryIDs = append(entryIDs, evaluation.EntryID)
	}
	slices.Sort(entryIDs)
	args := []any{characterID, conversationID}
	for _, id := range entryIDs {
		args = append(args, id)
	}
	rows, err := tx.QueryContext(ctx, `
SELECT id, status, feedback_positive_count, feedback_partial_count,
       feedback_negative_count, feedback_quarantined_until_ms
FROM social_memory_entries
WHERE character_id = ? AND conversation_id = ?
  AND kind IN (`+socialMemoryRecallKinds+`)
  AND id IN (`+sqlPlaceholders(len(entryIDs))+`)
ORDER BY id
FOR UPDATE`, args...)
	if err != nil {
		return nil, fmt.Errorf("locking SeekDB social feedback entries: %w", err)
	}
	defer rows.Close()
	states := make(map[string]socialFeedbackEntryState, len(entryIDs))
	for rows.Next() {
		var id string
		var state socialFeedbackEntryState
		var quarantine sql.NullInt64
		if err := rows.Scan(&id, &state.status, &state.positiveCount, &state.partialCount, &state.negativeCount, &quarantine); err != nil {
			return nil, fmt.Errorf("scanning SeekDB social feedback entry state: %w", err)
		}
		if quarantine.Valid {
			until := quarantine.Int64
			state.quarantinedUntil = &until
		}
		states[id] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SeekDB social feedback entry states: %w", err)
	}
	if len(states) != len(entryIDs) {
		return nil, errors.New("social feedback entries do not belong to the conversation")
	}
	return states, nil
}

func recordSeekDBSocialFeedbackBatch(
	ctx context.Context,
	tx seekDBTx,
	input SocialFeedbackBatchInput,
	states map[string]socialFeedbackEntryState,
	now int64,
) (SocialFeedbackBatchResult, error) {
	result := SocialFeedbackBatchResult{Events: make([]SocialFeedbackEvent, 0, len(input.Evaluations)), NoChange: true}
	for _, evaluation := range input.Evaluations {
		evaluation.EvidenceMessageIDs = append([]string{}, evaluation.EvidenceMessageIDs...)
		slices.Sort(evaluation.EvidenceMessageIDs)
		event, exists, err := loadSeekDBSocialFeedbackEvent(ctx, tx, input.TurnID, evaluation.EntryID)
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
		event, err = insertSeekDBSocialFeedbackEvent(ctx, tx, input, evaluation, now)
		if err != nil {
			return SocialFeedbackBatchResult{}, err
		}
		if err := applySeekDBSocialFeedbackAggregate(ctx, tx, evaluation, states[evaluation.EntryID], now); err != nil {
			return SocialFeedbackBatchResult{}, err
		}
		result.NoChange = false
		result.Events = append(result.Events, event)
	}
	return result, nil
}

func loadSeekDBSocialFeedbackEvent(ctx context.Context, tx seekDBTx, turnID, entryID string) (SocialFeedbackEvent, bool, error) {
	event, err := scanSeekDBSocialFeedbackEvent(tx.QueryRowContext(ctx, `
SELECT id, character_id, conversation_id, turn_id, entry_id, adoption, outcome, credit,
       evidence_message_ids, observed_message_count, evaluator_revision, created_at_ms
FROM social_memory_feedback_events WHERE turn_id = ? AND entry_id = ?`, turnID, entryID))
	if errors.Is(err, sql.ErrNoRows) {
		return SocialFeedbackEvent{}, false, nil
	}
	if err != nil {
		return SocialFeedbackEvent{}, false, fmt.Errorf("loading SeekDB social feedback event: %w", err)
	}
	return event, true, nil
}

func insertSeekDBSocialFeedbackEvent(
	ctx context.Context,
	tx seekDBTx,
	input SocialFeedbackBatchInput,
	evaluation SocialFeedbackEvaluation,
	now int64,
) (SocialFeedbackEvent, error) {
	evidenceJSON, err := json.Marshal(evaluation.EvidenceMessageIDs)
	if err != nil {
		return SocialFeedbackEvent{}, fmt.Errorf("encoding social feedback evidence IDs: %w", err)
	}
	id := newID()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO social_memory_feedback_events(
  id, character_id, conversation_id, turn_id, entry_id, adoption, outcome, credit,
  evidence_message_ids, observed_message_count, evaluator_revision, created_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, input.CharacterID, input.ConversationID, input.TurnID, evaluation.EntryID,
		evaluation.Adoption, evaluation.Outcome, evaluation.Credit, evidenceJSON,
		input.ObservedMessageCount, input.EvaluatorRevision, now,
	); err != nil {
		return SocialFeedbackEvent{}, fmt.Errorf("inserting SeekDB social feedback event: %w", err)
	}
	return scanSeekDBSocialFeedbackEvent(tx.QueryRowContext(ctx, `
SELECT id, character_id, conversation_id, turn_id, entry_id, adoption, outcome, credit,
       evidence_message_ids, observed_message_count, evaluator_revision, created_at_ms
FROM social_memory_feedback_events WHERE id = ?`, id))
}

func scanSeekDBSocialFeedbackEvent(row scanner) (SocialFeedbackEvent, error) {
	var event SocialFeedbackEvent
	var evidenceJSON []byte
	if err := row.Scan(
		&event.ID, &event.CharacterID, &event.ConversationID, &event.TurnID, &event.EntryID,
		&event.Adoption, &event.Outcome, &event.Credit, &evidenceJSON,
		&event.ObservedMessageCount, &event.EvaluatorRevision, &event.CreatedAtUnixMS,
	); err != nil {
		return SocialFeedbackEvent{}, err
	}
	if evidenceJSON == nil {
		event.EvidenceMessageIDs = []string{}
		return event, nil
	}
	if err := json.Unmarshal(evidenceJSON, &event.EvidenceMessageIDs); err != nil {
		return SocialFeedbackEvent{}, fmt.Errorf("decoding stored social feedback evidence IDs: %w", err)
	}
	if event.EvidenceMessageIDs == nil {
		event.EvidenceMessageIDs = []string{}
	}
	return event, nil
}

func applySeekDBSocialFeedbackAggregate(
	ctx context.Context,
	tx seekDBTx,
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
	var quarantinedUntil any
	if state.quarantinedUntil != nil {
		quarantinedUntil = *state.quarantinedUntil
	}
	if positive+partial > 0 && status == "suppressed" {
		status = "active"
		quarantinedUntil = nil
	} else if negative > 0 && nextNegative >= SocialNegativeSuppressThreshold && score <= SocialFeedbackQuarantineScore {
		until := now + SocialFeedbackQuarantineMS
		status = "suppressed"
		quarantinedUntil = until
	}
	changed, err := tx.ExecContext(ctx, `
UPDATE social_memory_entries
SET feedback_evaluation_count = feedback_evaluation_count + 1,
    feedback_adopted_count = feedback_adopted_count + ?,
    feedback_positive_count = ?,
    feedback_partial_count = ?,
    feedback_negative_count = ?,
    feedback_score_basis_points = ?,
    status = ?,
    feedback_quarantined_until_ms = ?,
    updated_at_ms = ?
WHERE id = ?`, adopted, nextPositive, nextPartial, nextNegative,
		score, status, quarantinedUntil, now, evaluation.EntryID)
	if err != nil {
		return fmt.Errorf("updating SeekDB social feedback aggregate: %w", err)
	}
	affected, err := changed.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading SeekDB social feedback aggregate rows: %w", err)
	}
	if affected != 1 {
		return errors.New("social feedback aggregate did not update exactly one entry")
	}
	return nil
}
