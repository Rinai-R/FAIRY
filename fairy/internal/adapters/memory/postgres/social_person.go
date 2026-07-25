package postgres

import (
	"context"
	"fmt"

	domainmemory "fairy/internal/domain/memory"
)

func UpsertSocialPersonNote(
	ctx context.Context,
	db RowQuerier,
	id, characterID, conversationID, senderID, senderName, note string,
	now int64,
) (domainmemory.SocialPersonNote, error) {
	var stored domainmemory.SocialPersonNote
	err := db.QueryRow(ctx, `
INSERT INTO social_person_notes (
  id, character_id, conversation_id, sender_id, sender_name, note, created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
ON CONFLICT (character_id, conversation_id, sender_id)
DO UPDATE SET sender_name = EXCLUDED.sender_name, note = EXCLUDED.note, updated_at_ms = EXCLUDED.updated_at_ms
RETURNING id, character_id, conversation_id, sender_id, sender_name, note, updated_at_ms`,
		id, characterID, conversationID, senderID, senderName, note, now,
	).Scan(&stored.ID, &stored.CharacterID, &stored.ConversationID, &stored.SenderID, &stored.SenderName, &stored.Note, &stored.UpdatedAtUnixMS)
	if err != nil {
		return domainmemory.SocialPersonNote{}, fmt.Errorf("upserting social person note: %w", err)
	}
	return stored, nil
}

func ListSocialPersonNotes(ctx context.Context, db Querier, characterID, conversationID string, senderIDs []string, limit int) ([]domainmemory.SocialPersonNote, error) {
	rows, err := db.Query(ctx, `
SELECT id, character_id, conversation_id, sender_id, sender_name, note, updated_at_ms
FROM social_person_notes
WHERE character_id = $1 AND conversation_id = $2 AND sender_id = ANY($3)
ORDER BY updated_at_ms DESC, id ASC
LIMIT $4`, characterID, conversationID, senderIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("listing social person notes: %w", err)
	}
	defer rows.Close()
	notes := make([]domainmemory.SocialPersonNote, 0, len(senderIDs))
	for rows.Next() {
		var note domainmemory.SocialPersonNote
		if scanErr := rows.Scan(&note.ID, &note.CharacterID, &note.ConversationID, &note.SenderID, &note.SenderName, &note.Note, &note.UpdatedAtUnixMS); scanErr != nil {
			return nil, fmt.Errorf("scanning social person note: %w", scanErr)
		}
		notes = append(notes, note)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating social person notes: %w", err)
	}
	return notes, nil
}
