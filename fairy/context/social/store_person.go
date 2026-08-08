package social

import (
	"context"
	"strings"
	"time"
)

func (s *Store) upsertSocialPersonNotePostgres(ctx context.Context, input SocialPersonNoteInput) (SocialPersonNote, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	if err := VerifySocialConversationScope(queryCtx, s.pool.Raw(), input.CharacterID, input.ConversationID); err != nil {
		return SocialPersonNote{}, err
	}
	return UpsertSocialPersonNote(
		queryCtx,
		s.pool.Raw(),
		newID(),
		input.CharacterID,
		input.ConversationID,
		strings.TrimSpace(input.SenderID),
		strings.TrimSpace(input.SenderName),
		strings.TrimSpace(input.Note),
		time.Now().UnixMilli(),
	)
}

func (s *Store) listSocialPersonNotesPostgres(ctx context.Context, characterID, conversationID string, senderIDs []string) ([]SocialPersonNote, error) {
	queryCtx, cancel := s.pool.QueryContext(ctx)
	defer cancel()
	if err := VerifySocialConversationScope(queryCtx, s.pool.Raw(), characterID, conversationID); err != nil {
		return nil, err
	}
	return ListSocialPersonNotes(queryCtx, s.pool.Raw(), characterID, conversationID, senderIDs, maxSocialPersonNotes)
}
