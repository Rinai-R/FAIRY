package social

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) upsertSocialPersonNoteSeekDB(ctx context.Context, input SocialPersonNoteInput) (SocialPersonNote, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	tx, err := s.seekDB.BeginTx(queryCtx, nil)
	if err != nil {
		return SocialPersonNote{}, fmt.Errorf("beginning SeekDB social person note transaction: %w", err)
	}
	defer tx.Rollback()
	if err := lockSeekDBSocialConversation(queryCtx, tx, input.CharacterID, input.ConversationID); err != nil {
		return SocialPersonNote{}, err
	}
	senderID := strings.TrimSpace(input.SenderID)
	senderName := strings.TrimSpace(input.SenderName)
	note := strings.TrimSpace(input.Note)
	recallCue := senderName
	if recallCue == "" {
		recallCue = senderID
	}
	digest := socialMemoryContentDigest(SocialMemoryEntryInput{
		Kind: "person_note", Situation: senderID, Content: note, RecallCue: recallCue,
	})
	now := s.currentUnixMS()
	var existingID string
	err = tx.QueryRowContext(queryCtx, `
SELECT id FROM social_memory_entries
WHERE character_id = ? AND conversation_id = ? AND sender_id = ? AND kind = 'person_note'
FOR UPDATE`, input.CharacterID, input.ConversationID, senderID).Scan(&existingID)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(queryCtx, `
UPDATE social_memory_entries
SET sender_name = ?, content = ?, recall_cue = ?, content_hash = ?,
    source_start_ms = ?, source_end_ms = ?, updated_at_ms = ?
WHERE id = ?`, senderName, note, recallCue, digest[:], now, now, now, existingID); err != nil {
			return SocialPersonNote{}, fmt.Errorf("updating SeekDB social person note: %w", err)
		}
	case errors.Is(err, sql.ErrNoRows):
		existingID = newID()
		if _, err := tx.ExecContext(queryCtx, `
INSERT INTO social_memory_entries(
  id, character_id, conversation_id, kind, situation, content, recall_cue,
  content_hash, sender_id, sender_name, status, source_start_ms, source_end_ms,
  feedback_evaluation_count, feedback_adopted_count, feedback_positive_count,
  feedback_partial_count, feedback_negative_count, feedback_score_basis_points,
  created_at_ms, updated_at_ms
) VALUES (?, ?, ?, 'person_note', ?, ?, ?, ?, ?, ?, 'active', ?, ?, 0, 0, 0, 0, 0, 0, ?, ?)`,
			existingID, input.CharacterID, input.ConversationID, senderID, note, recallCue,
			digest[:], senderID, senderName, now, now, now, now,
		); err != nil {
			return SocialPersonNote{}, fmt.Errorf("inserting SeekDB social person note: %w", err)
		}
	default:
		return SocialPersonNote{}, fmt.Errorf("locking SeekDB social person note: %w", err)
	}
	stored, err := scanSeekDBSocialPersonNote(tx.QueryRowContext(queryCtx, `
SELECT id, character_id, conversation_id, sender_id, sender_name, content, updated_at_ms
FROM social_memory_entries WHERE id = ?`, existingID))
	if err != nil {
		return SocialPersonNote{}, err
	}
	if err := tx.Commit(); err != nil {
		return SocialPersonNote{}, fmt.Errorf("committing SeekDB social person note transaction: %w", err)
	}
	return stored, nil
}

func (s *Store) listSocialPersonNotesSeekDB(
	ctx context.Context,
	characterID, conversationID string,
	senderIDs []string,
) ([]SocialPersonNote, error) {
	queryCtx, cancel := s.seekDBQueryContext(ctx)
	defer cancel()
	if err := verifySeekDBSocialConversation(queryCtx, s.seekDB, characterID, conversationID, false); err != nil {
		return nil, err
	}
	args := []any{characterID, conversationID}
	for _, id := range senderIDs {
		args = append(args, id)
	}
	args = append(args, maxSocialPersonNotes)
	rows, err := s.seekDB.QueryContext(queryCtx, `
SELECT id, character_id, conversation_id, sender_id, sender_name, content, updated_at_ms
FROM social_memory_entries
WHERE character_id = ? AND conversation_id = ?
  AND kind = 'person_note'
  AND sender_id IN (`+sqlPlaceholders(len(senderIDs))+`)
ORDER BY updated_at_ms DESC, id ASC
LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("listing SeekDB social person notes: %w", err)
	}
	defer rows.Close()
	notes := make([]SocialPersonNote, 0, len(senderIDs))
	for rows.Next() {
		note, scanErr := scanSeekDBSocialPersonNote(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		notes = append(notes, note)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating SeekDB social person notes: %w", err)
	}
	return notes, nil
}

func scanSeekDBSocialPersonNote(row scanner) (SocialPersonNote, error) {
	var note SocialPersonNote
	if err := row.Scan(
		&note.ID, &note.CharacterID, &note.ConversationID, &note.SenderID, &note.SenderName, &note.Note, &note.UpdatedAtUnixMS,
	); err != nil {
		return SocialPersonNote{}, fmt.Errorf("scanning SeekDB social person note: %w", err)
	}
	return note, nil
}
