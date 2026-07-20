package memory

import (
	"context"
)

type CompactionResult struct {
	WindowRevision        uint64 `json:"windowRevision"`
	RetainedDialogueItems int    `json:"retainedDialogueItems"`
}

func (s *Store) CommitPromptWindow(conversationID string, expectedRevision uint64, summary string) (CompactionResult, error) {
	return s.CommitPromptWindowContext(context.Background(), conversationID, expectedRevision, summary)
}

func (s *Store) CommitPromptWindowContext(ctx context.Context, conversationID string, expectedRevision uint64, summary string) (CompactionResult, error) {
	return s.commitPromptWindowPostgres(ctx, conversationID, expectedRevision, summary)
}
