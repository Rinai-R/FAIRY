package social

import (
	"context"
	"fmt"
)

func UpsertSocialPersonNote(
	ctx context.Context,
	db RowQuerier,
	id, characterID, conversationID, senderID, senderName, note string,
	now int64,
) (SocialPersonNote, error) {
	var stored SocialPersonNote
	recallCue := senderName
	if recallCue == "" {
		recallCue = senderID
	}
	hash := SocialMemoryContentHash(SocialMemoryEntryInput{
		Kind: "person_note", Situation: senderID, Content: note, RecallCue: recallCue,
	})
	err := db.QueryRow(ctx, `
INSERT INTO social_memory_entries (
  id, character_id, conversation_id, kind, situation, content, recall_cue,
  content_hash, sender_id, sender_name, status, source_start_ms, source_end_ms,
  created_at_ms, updated_at_ms
) VALUES ($1, $2, $3, 'person_note', $4, $6, $8, $9, $4, $5, 'active', $7, $7, $7, $7)
ON CONFLICT (character_id, conversation_id, sender_id)
DO UPDATE SET sender_name = EXCLUDED.sender_name,
              content = EXCLUDED.content,
              recall_cue = EXCLUDED.recall_cue,
              content_hash = EXCLUDED.content_hash,
              source_end_ms = EXCLUDED.source_end_ms,
              updated_at_ms = EXCLUDED.updated_at_ms
RETURNING id, character_id, conversation_id, sender_id, sender_name, content, updated_at_ms`,
		id, characterID, conversationID, senderID, senderName, note, now, recallCue, hash,
	).Scan(&stored.ID, &stored.CharacterID, &stored.ConversationID, &stored.SenderID, &stored.SenderName, &stored.Note, &stored.UpdatedAtUnixMS)
	if err != nil {
		return SocialPersonNote{}, fmt.Errorf("upserting social person note: %w", err)
	}
	return stored, nil
}

func ListSocialPersonNotes(ctx context.Context, db Querier, characterID, conversationID string, senderIDs []string, limit int) ([]SocialPersonNote, error) {
	rows, err := db.Query(ctx, `
SELECT id, character_id, conversation_id, sender_id, sender_name, content, updated_at_ms
FROM social_memory_entries
WHERE character_id = $1 AND conversation_id = $2 AND sender_id = ANY($3)
  AND kind = 'person_note'
ORDER BY updated_at_ms DESC, id ASC
LIMIT $4`, characterID, conversationID, senderIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("listing social person notes: %w", err)
	}
	defer rows.Close()
	notes := make([]SocialPersonNote, 0, len(senderIDs))
	for rows.Next() {
		var note SocialPersonNote
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
